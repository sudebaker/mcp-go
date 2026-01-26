#!/usr/bin/env python3
"""
Safe file operations for sandbox execution.

Provides secure wrappers for file I/O operations with automatic
path validation and security controls.
"""

import io
import os
import sys
from pathlib import Path
from typing import Any, Optional, Union
import logging

# Add parent dir to path for imports
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

try:
    import pandas as pd

    PANDAS_AVAILABLE = True
except ImportError:
    PANDAS_AVAILABLE = False

from common.validators import (
    validate_read_path,
    validate_write_path,
    list_files,
    PathValidationError,
)

logger = logging.getLogger(__name__)


class SafeFileOperations:
    """
    Secure file operations manager for sandbox execution.

    Automatically validates all file paths against allowed directories
    and provides safe wrappers for common operations.
    """

    def __init__(
        self,
        readonly_dir: str = "/data/input",
        writable_dir: str = "/data/output",
        max_file_size_mb: int = 100,
    ):
        self.readonly_dir = readonly_dir
        self.writable_dir = writable_dir
        self.max_file_size_mb = max_file_size_mb

        self.readonly_path = Path(readonly_dir).resolve()
        self.writable_path = Path(writable_dir).resolve()

    def _resolve_read_path(self, filepath: Union[str, Path]) -> Path:
        """Resolve and validate a path for reading."""
        if not os.path.isabs(str(filepath)):
            filepath = self.readonly_path / filepath
        return validate_read_path(str(filepath), self.readonly_dir)

    def _resolve_write_path(self, filepath: Union[str, Path]) -> Path:
        """Resolve and validate a path for writing."""
        if not os.path.isabs(str(filepath)):
            filepath = self.writable_path / filepath
        return validate_write_path(
            str(filepath), self.writable_dir, max_size_mb=self.max_file_size_mb
        )

    def open_read(self, filepath: str, mode: str = "r", **kwargs) -> io.IOBase:
        """
        Safely open a file for reading.

        Args:
            filepath: Path to file (relative to readonly_dir or absolute)
            mode: File mode ('r', 'rb', etc.)
            **kwargs: Additional arguments to pass to open()

        Returns:
            File handle

        Raises:
            PathValidationError: If path is invalid
            PermissionError: If file is not readable
        """
        if "w" in mode or "a" in mode or "+" in mode:
            raise ValueError(f"Write mode not allowed in open_read: {mode}")

        validated_path = self._resolve_read_path(filepath)

        file_size_mb = validated_path.stat().st_size / (1024 * 1024)
        if file_size_mb > self.max_file_size_mb:
            raise ValueError(
                f"File too large: {file_size_mb:.2f}MB (max: {self.max_file_size_mb}MB)"
            )

        logger.info(f"Opening file for reading: {validated_path}")
        return open(validated_path, mode, **kwargs)

    def open_write(self, filepath: str, mode: str = "w", **kwargs) -> io.IOBase:
        """
        Safely open a file for writing.

        Args:
            filepath: Path to file (relative to writable_dir or absolute)
            mode: File mode ('w', 'wb', 'a', etc.)
            **kwargs: Additional arguments to pass to open()

        Returns:
            File handle

        Raises:
            PathValidationError: If path is invalid
            PermissionError: If directory is not writable
        """
        if mode not in ("w", "wb", "a", "ab", "x", "xb"):
            raise ValueError(f"Invalid write mode: {mode}")

        validated_path = self._resolve_write_path(filepath)

        logger.info(f"Opening file for writing: {validated_path}")
        return open(validated_path, mode, **kwargs)

    def read_text(self, filepath: str, encoding: str = "utf-8") -> str:
        """Read entire text file safely."""
        validated_path = self._resolve_read_path(filepath)

        file_size_mb = validated_path.stat().st_size / (1024 * 1024)
        if file_size_mb > self.max_file_size_mb:
            raise ValueError(f"File too large: {file_size_mb:.2f}MB")

        return validated_path.read_text(encoding=encoding)

    def read_bytes(self, filepath: str) -> bytes:
        """Read entire binary file safely."""
        validated_path = self._resolve_read_path(filepath)

        file_size_mb = validated_path.stat().st_size / (1024 * 1024)
        if file_size_mb > self.max_file_size_mb:
            raise ValueError(f"File too large: {file_size_mb:.2f}MB")

        return validated_path.read_bytes()

    def write_text(self, filepath: str, content: str, encoding: str = "utf-8"):
        """Write text to file safely."""
        validated_path = self._resolve_write_path(filepath)

        content_size_mb = len(content.encode(encoding)) / (1024 * 1024)
        if content_size_mb > self.max_file_size_mb:
            raise ValueError(f"Content too large: {content_size_mb:.2f}MB")

        validated_path.write_text(content, encoding=encoding)
        logger.info(f"Wrote text file: {validated_path}")

    def write_bytes(self, filepath: str, content: bytes):
        """Write bytes to file safely."""
        validated_path = self._resolve_write_path(filepath)

        content_size_mb = len(content) / (1024 * 1024)
        if content_size_mb > self.max_file_size_mb:
            raise ValueError(f"Content too large: {content_size_mb:.2f}MB")

        validated_path.write_bytes(content)
        logger.info(f"Wrote binary file: {validated_path}")

    def list_input_files(self, pattern: str = "*", subdir: str = "") -> list[Path]:
        """
        List files in the readonly directory.

        Args:
            pattern: Glob pattern to match files
            subdir: Subdirectory within readonly_dir

        Returns:
            List of Path objects
        """
        return list_files(subdir, self.readonly_dir, pattern)

    def list_output_files(self, pattern: str = "*") -> list[Path]:
        """List files in the writable directory."""
        try:
            return sorted(self.writable_path.glob(pattern))
        except Exception as e:
            logger.error(f"Error listing output files: {e}")
            return []

    def file_exists(self, filepath: str, check_writable: bool = False) -> bool:
        """Check if file exists (in readonly or writable dir)."""
        try:
            if check_writable:
                path = self.writable_path / filepath
            else:
                path = self.readonly_path / filepath

            return path.exists() and path.is_file()
        except Exception:
            return False

    def get_file_info(self, filepath: str) -> dict[str, Any]:
        """Get safe file information."""
        try:
            validated_path = self._resolve_read_path(filepath)
            stat = validated_path.stat()

            return {
                "name": validated_path.name,
                "size_bytes": stat.st_size,
                "size_mb": round(stat.st_size / (1024 * 1024), 2),
                "modified": stat.st_mtime,
                "is_file": validated_path.is_file(),
                "suffix": validated_path.suffix,
            }
        except Exception as e:
            logger.error(f"Error getting file info: {e}")
            return {}

    # Pandas integration

    def read_csv(self, filepath: str, **kwargs) -> Optional["pd.DataFrame"]:
        """Safely read CSV file with pandas."""
        if not PANDAS_AVAILABLE:
            raise RuntimeError("pandas not available")

        validated_path = self._resolve_read_path(filepath)
        logger.info(f"Reading CSV: {validated_path}")
        return pd.read_csv(validated_path, **kwargs)

    def read_excel(self, filepath: str, **kwargs) -> Optional["pd.DataFrame"]:
        """Safely read Excel file with pandas."""
        if not PANDAS_AVAILABLE:
            raise RuntimeError("pandas not available")

        validated_path = self._resolve_read_path(filepath)
        logger.info(f"Reading Excel: {validated_path}")
        return pd.read_excel(validated_path, **kwargs)

    def read_json(self, filepath: str, **kwargs) -> Optional["pd.DataFrame"]:
        """Safely read JSON file with pandas."""
        if not PANDAS_AVAILABLE:
            raise RuntimeError("pandas not available")

        validated_path = self._resolve_read_path(filepath)
        logger.info(f"Reading JSON: {validated_path}")
        return pd.read_json(validated_path, **kwargs)

    def to_csv(self, df: "pd.DataFrame", filepath: str, **kwargs):
        """Safely write DataFrame to CSV."""
        if not PANDAS_AVAILABLE:
            raise RuntimeError("pandas not available")

        validated_path = self._resolve_write_path(filepath)
        logger.info(f"Writing CSV: {validated_path}")
        df.to_csv(validated_path, **kwargs)

    def to_excel(self, df: "pd.DataFrame", filepath: str, **kwargs):
        """Safely write DataFrame to Excel."""
        if not PANDAS_AVAILABLE:
            raise RuntimeError("pandas not available")

        validated_path = self._resolve_write_path(filepath)
        logger.info(f"Writing Excel: {validated_path}")
        df.to_excel(validated_path, **kwargs)

    def to_json(self, df: "pd.DataFrame", filepath: str, **kwargs):
        """Safely write DataFrame to JSON."""
        if not PANDAS_AVAILABLE:
            raise RuntimeError("pandas not available")

        validated_path = self._resolve_write_path(filepath)
        logger.info(f"Writing JSON: {validated_path}")
        df.to_json(validated_path, **kwargs)


def create_safe_file_ops(
    readonly_dir: str = "/data/input",
    writable_dir: str = "/data/output",
    max_file_size_mb: int = 100,
) -> SafeFileOperations:
    """Factory function to create SafeFileOperations instance."""
    return SafeFileOperations(readonly_dir, writable_dir, max_file_size_mb)
