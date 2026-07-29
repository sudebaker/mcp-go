#!/usr/bin/env python3
"""Structured logging module for MCP Python tools.

Provides JSON-structured logging compatible with log aggregation systems.
"""

import json
import logging
import re
import sys
from datetime import datetime, timezone
from typing import Any, Dict, Optional, Set


class StructuredLogger:
    """JSON-structured logger for MCP tools with sanitization."""

    SENSITIVE_KEYS: Set[str] = {
        "password",
        "secret",
        "token",
        "api_key",
        "apikey",
        "auth",
        "authorization",
        "credential",
        "private_key",
        "access_token",
        "refresh_token",
        "session_id",
        "cookie",
    }

    MAX_MESSAGE_LENGTH = 10000
    MAX_FIELD_LENGTH = 1000

    def __init__(
        self,
        name: str,
        level: int = logging.INFO,
        tool_name: Optional[str] = None,
    ):
        self.logger = logging.getLogger(name)
        self.logger.setLevel(level)
        self.tool_name = tool_name or name
        self._ensure_handler()

    def _ensure_handler(self) -> None:
        if not self.logger.handlers:
            handler = logging.StreamHandler(sys.stderr)
            handler.setLevel(self.logger.level)
            handler.setFormatter(self._JSONFormatter(tool_name=self.tool_name))
            self.logger.addHandler(handler)

    @staticmethod
    def _sanitize_value(value: Any, max_length: int = MAX_FIELD_LENGTH) -> Any:
        """Sanitize log values to prevent injection and information disclosure."""
        if isinstance(value, str):
            if len(value) > max_length:
                value = value[:max_length] + "...[truncated]"

            value = re.sub(r"[\x00-\x1f\x7f-\x9f]", "", value)

            return value

        elif isinstance(value, (list, tuple)):
            return [
                StructuredLogger._sanitize_value(v, max_length) for v in value[:100]
            ]

        elif isinstance(value, dict):
            return StructuredLogger._sanitize_dict(value, max_length)

        return value

    @staticmethod
    def _sanitize_dict(
        data: Dict[str, Any], max_length: int = MAX_FIELD_LENGTH
    ) -> Dict[str, Any]:
        """Sanitize dictionary, redacting sensitive keys."""
        sanitized = {}
        for key, value in data.items():
            key_lower = str(key).lower()

            is_sensitive = any(
                sensitive in key_lower for sensitive in StructuredLogger.SENSITIVE_KEYS
            )

            if is_sensitive:
                sanitized[key] = "***REDACTED***"
            else:
                sanitized[key] = StructuredLogger._sanitize_value(value, max_length)

        return sanitized

    class _JSONFormatter(logging.Formatter):
        """Custom formatter that outputs JSON log records."""

        def __init__(self, tool_name: str = "mcp-tool"):
            super().__init__()
            self.tool_name = tool_name

        def format(self, record: logging.LogRecord) -> str:
            message = record.getMessage()
            if len(message) > StructuredLogger.MAX_MESSAGE_LENGTH:
                message = (
                    message[: StructuredLogger.MAX_MESSAGE_LENGTH] + "...[truncated]"
                )

            log_data: Dict[str, Any] = {
                "timestamp": datetime.now(timezone.utc).isoformat(),
                "level": record.levelname,
                "logger": self.tool_name,
                "message": message,
            }

            if record.exc_info:
                log_data["exception"] = self.formatException(record.exc_info)

            if hasattr(record, "extra_data"):
                extra_data = getattr(record, "extra_data")
                if extra_data:
                    sanitized_extra = StructuredLogger._sanitize_dict(extra_data)
                    log_data.update(sanitized_extra)

            if record.name != self.tool_name:
                log_data["module"] = record.name

            return json.dumps(log_data, default=str, ensure_ascii=False)

    def log(
        self,
        level: int,
        msg: str,
        extra_data: Optional[Dict[str, Any]] = None,
        **kwargs: Any,
    ) -> None:
        extra = {"extra_data": extra_data or {}}
        self.logger.log(level, msg, extra=extra, **kwargs)

    def debug(
        self,
        msg: str,
        extra_data: Optional[Dict[str, Any]] = None,
        **kwargs: Any,
    ) -> None:
        self.log(logging.DEBUG, msg, extra_data, **kwargs)

    def info(
        self,
        msg: str,
        extra_data: Optional[Dict[str, Any]] = None,
        **kwargs: Any,
    ) -> None:
        self.log(logging.INFO, msg, extra_data, **kwargs)

    def warning(
        self,
        msg: str,
        extra_data: Optional[Dict[str, Any]] = None,
        **kwargs: Any,
    ) -> None:
        self.log(logging.WARNING, msg, extra_data, **kwargs)

    def error(
        self,
        msg: str,
        extra_data: Optional[Dict[str, Any]] = None,
        exc_info: bool = False,
        **kwargs: Any,
    ) -> None:
        if exc_info:
            self.logger.error(
                msg, exc_info=True, extra={"extra_data": extra_data or {}}, **kwargs
            )
        else:
            self.log(logging.ERROR, msg, extra_data, **kwargs)

    def critical(
        self,
        msg: str,
        extra_data: Optional[Dict[str, Any]] = None,
        **kwargs: Any,
    ) -> None:
        self.log(logging.CRITICAL, msg, extra_data, **kwargs)


class RequestLogger:
    """Logger for HTTP requests with timing and status tracking."""

    def __init__(self, tool_name: str):
        self.logger = StructuredLogger(f"{tool_name}_requests")
        self.tool_name = tool_name

    def log_request(
        self,
        method: str,
        url: str,
        status_code: Optional[int] = None,
        duration_seconds: Optional[float] = None,
        error: Optional[str] = None,
    ) -> None:
        """Log an HTTP request.

        Args:
            method: HTTP method (GET, POST, etc.)
            url: Request URL
            status_code: Response status code
            duration_seconds: Request duration in seconds
            error: Error message if request failed

        """
        data = {
            "http_method": method,
            "url": url,
            "status_code": status_code,
            "duration_seconds": round(duration_seconds, 4)
            if duration_seconds
            else None,
        }

        if error:
            data["error"] = error
            self.logger.warning(f"HTTP request failed: {method} {url}", extra_data=data)
        elif status_code and status_code >= 400:
            data["error"] = f"HTTP {status_code}"
            self.logger.warning(f"HTTP request error: {method} {url}", extra_data=data)
        else:
            self.logger.debug(f"HTTP request success: {method} {url}", extra_data=data)


def get_logger(name: str, tool_name: Optional[str] = None) -> StructuredLogger:
    """Get a structured logger instance.

    Args:
        name: Logger name (usually __name__)
        tool_name: Optional tool name override

    Returns:
        StructuredLogger instance

    """
    return StructuredLogger(name, tool_name=tool_name)
