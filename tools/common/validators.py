import ipaddress
import os
import re
from pathlib import Path
from typing import Optional


class PathValidationError(ValueError):
    """Exception raised for path validation errors."""

    pass


# --- SSRF / Internal URL Protection ---

INTERNAL_IP_PATTERNS = [
    r"^127\.",
    r"^10\.",
    r"^172\.(1[6-9]|2\d|3[01])\.",
    r"^192\.168\.",
    r"^169\.254\.",
    r"^0\.",
    r"^localhost$",
    r"^::1$",
    r"^fe80:",
    r"^fc00:",
    r"^fd00:",
]

BLOCKED_HOSTS = [
    "169.254.169.254",  # AWS / Azure metadata
    "metadata.google.internal",  # GCP metadata
    "metadata.googleusercontent.com",
    "instance-data",  # OpenStack metadata
]

INTERNAL_DOMAIN_PATTERNS = [
    r".*\.local$",
    r".*\.localhost$",
    r".*\.internal$",
]

_compiled_ip_patterns = [re.compile(p) for p in INTERNAL_IP_PATTERNS]
_compiled_domain_patterns = [
    re.compile(p, re.IGNORECASE) for p in INTERNAL_DOMAIN_PATTERNS
]


def _load_ssrf_allowlist() -> tuple[list[ipaddress._BaseNetwork], list[str]]:
    """
    Parse SSRF_ALLOWLIST env var into (networks, hostnames).

    Format: comma-separated list of CIDR ranges and/or hostnames.
    Example: SSRF_ALLOWLIST=192.168.1.0/24,10.0.0.0/8,myservice.corp
    """
    raw = os.environ.get("SSRF_ALLOWLIST", "").strip()
    if not raw:
        return [], []

    networks: list[ipaddress._BaseNetwork] = []
    hostnames: list[str] = []

    for entry in raw.split(","):
        entry = entry.strip()
        if not entry:
            continue
        try:
            networks.append(ipaddress.ip_network(entry, strict=False))
        except ValueError:
            hostnames.append(entry.lower())

    return networks, hostnames


def _is_allowlisted(host: str) -> bool:
    """Return True if the host matches an entry in SSRF_ALLOWLIST."""
    allowed_networks, allowed_hosts = _load_ssrf_allowlist()

    if host in allowed_hosts:
        return True

    try:
        addr = ipaddress.ip_address(host)
        for net in allowed_networks:
            if addr in net:
                return True
    except ValueError:
        # host is a domain name, not an IP — already checked above
        pass

    return False


def is_internal_url(url: str) -> bool:
    """
    Return True if the URL resolves to an internal / private network address.

    Blocks:
    - Loopback (127.x, ::1, localhost)
    - RFC-1918 private ranges (10.x, 172.16-31.x, 192.168.x)
    - Link-local (169.254.x, fe80:, fc00:, fd00:)
    - Cloud metadata endpoints (169.254.169.254, metadata.google.internal, …)
    - .local / .localhost / .internal domains
    - Multicast / reserved first octets (0.x, 224+)

    Always returns True on parse errors so callers can safely treat failures
    as blocked.
    """
    try:
        from urllib.parse import urlparse

        parsed = urlparse(url)
        host = (parsed.hostname or "").lower()

        if not host:
            return True

        # Cloud metadata endpoints are NEVER allowable, regardless of SSRF_ALLOWLIST
        if host in BLOCKED_HOSTS:
            return True

        if host == "localhost" or host == "::1":
            # Allow only if explicitly in the allowlist
            if not _is_allowlisted(host):
                return True

        for pattern in _compiled_ip_patterns:
            if pattern.match(host):
                # Private/internal IP: allow if explicitly allowlisted
                if not _is_allowlisted(host):
                    return True
                return False

        for pattern in _compiled_domain_patterns:
            if pattern.match(host):
                if not _is_allowlisted(host):
                    return True
                return False

        # Numeric IPv4 with reserved first octet (multicast 224+ or 0.x)
        if host.replace(".", "").isdigit():
            parts = host.split(".")
            if len(parts) == 4:
                try:
                    first_octet = int(parts[0])
                    if first_octet == 0 or first_octet >= 224:
                        return True
                except ValueError:
                    pass

        return False
    except Exception:
        return True


# --- Path validation helpers ---


def validate_file_path(file_path: str, allowed_dir: str = "/data") -> Path:
    """
    Validate that a file path is safely inside the allowed directory.

    Args:
        file_path: Path of the file to validate.
        allowed_dir: Base directory that must contain the resolved path.

    Returns:
        Resolved Path object.

    Raises:
        PathValidationError: On path traversal or invalid directory.
        FileNotFoundError: If the file does not exist.
        PermissionError: If the file is not readable.
    """
    try:
        allowed = Path(allowed_dir).resolve(strict=True)
    except (OSError, RuntimeError) as e:
        raise PathValidationError(f"Invalid allowed directory: {allowed_dir}") from e

    try:
        path = Path(file_path).resolve(strict=True)
    except FileNotFoundError:
        raise FileNotFoundError(f"File not found: {file_path}")
    except (OSError, RuntimeError) as e:
        raise PathValidationError(f"Invalid file path: {file_path}") from e

    if not path.is_relative_to(allowed):
        raise PathValidationError(f"Path traversal detected: {file_path}")

    if not os.access(path, os.R_OK):
        raise PermissionError(f"File not readable: {file_path}")

    return path


def validate_output_path(
    output_path: str, allowed_dir: str = "/data", check_writable: bool = True
) -> Path:
    """
    Validate that an output path is safely inside the allowed directory.
    Creates parent directories if they do not exist.

    Args:
        output_path: Output path to validate.
        allowed_dir: Base directory that must contain the resolved path.
        check_writable: Whether to verify write permissions.

    Returns:
        Resolved Path object.

    Raises:
        PathValidationError: On path traversal or invalid directory.
        PermissionError: If write permissions are missing.
    """
    try:
        allowed = Path(allowed_dir).resolve(strict=True)
    except (OSError, RuntimeError) as e:
        raise PathValidationError(f"Invalid allowed directory: {allowed_dir}") from e

    path = Path(output_path).resolve()

    if not path.is_relative_to(allowed):
        raise PathValidationError(f"Path traversal detected: {output_path}")

    try:
        path.parent.mkdir(parents=True, exist_ok=True)
    except (OSError, PermissionError) as e:
        raise PermissionError(
            f"Cannot create parent directories for: {output_path}"
        ) from e

    if check_writable:
        test_dir = path.parent if path.exists() and path.is_dir() else path.parent
        if not os.access(test_dir, os.W_OK):
            raise PermissionError(f"Directory not writable: {test_dir}")

    return path


def sanitize_filename(filename: str, max_length: int = 255) -> str:
    """
    Sanitize a filename by removing dangerous characters.

    Args:
        filename: Filename to sanitize.
        max_length: Maximum allowed length after sanitization.

    Returns:
        Sanitized filename string.

    Raises:
        ValueError: If the filename is empty after sanitization.
    """
    sanitized = re.sub(r"[^\w\s.-]", "", filename)
    sanitized = sanitized.replace("..", "").strip()

    if not sanitized:
        raise ValueError("Filename is empty after sanitization")

    if len(sanitized) > max_length:
        name, ext = os.path.splitext(sanitized)
        sanitized = name[: max_length - len(ext)] + ext

    return sanitized


def validate_read_path(file_path: str, readonly_dir: str = "/data/input") -> Path:
    """
    Validate a path for READ-ONLY operations.

    Args:
        file_path: Path of the file to read.
        readonly_dir: Base read-only directory.

    Returns:
        Resolved Path object.

    Raises:
        PathValidationError: On path traversal or invalid directory.
        FileNotFoundError: If the file does not exist.
        PermissionError: If the file is not readable.
    """
    try:
        allowed = Path(readonly_dir).resolve(strict=True)
    except (OSError, RuntimeError) as e:
        raise PathValidationError(f"Invalid readonly directory: {readonly_dir}") from e

    try:
        path = Path(file_path).resolve(strict=True)
    except FileNotFoundError:
        raise FileNotFoundError(f"File not found: {file_path}")
    except (OSError, RuntimeError) as e:
        raise PathValidationError(f"Invalid file path: {file_path}") from e

    if not path.is_relative_to(allowed):
        raise PathValidationError(f"Path outside readonly directory: {file_path}")

    if not path.is_file():
        raise PathValidationError(f"Path is not a file: {file_path}")

    if not os.access(path, os.R_OK):
        raise PermissionError(f"File not readable: {file_path}")

    return path


def validate_write_path(
    file_path: str,
    writable_dir: str = "/data/output",
    create_parents: bool = True,
    max_size_mb: Optional[int] = None,
) -> Path:
    """
    Validate a path for WRITE operations.

    Args:
        file_path: Path of the file to write.
        writable_dir: Base writable directory.
        create_parents: Whether to create parent directories.
        max_size_mb: Maximum allowed file size in MB (checked if file already exists).

    Returns:
        Resolved Path object.

    Raises:
        PathValidationError: On path traversal or invalid directory.
        PermissionError: If write permissions are missing.
        ValueError: If the existing file exceeds the maximum size.
    """
    try:
        allowed = Path(writable_dir).resolve(strict=True)
    except (OSError, RuntimeError) as e:
        raise PathValidationError(f"Invalid writable directory: {writable_dir}") from e

    if not os.path.isabs(file_path):
        path = (allowed / file_path).resolve()
    else:
        path = Path(file_path).resolve()

    if not path.is_relative_to(allowed):
        raise PathValidationError(f"Path outside writable directory: {file_path}")

    if create_parents:
        try:
            path.parent.mkdir(parents=True, exist_ok=True)
        except (OSError, PermissionError) as e:
            raise PermissionError(
                f"Cannot create parent directories for: {file_path}"
            ) from e

    test_dir = path.parent
    if not os.access(test_dir, os.W_OK):
        raise PermissionError(f"Directory not writable: {test_dir}")

    if path.exists() and max_size_mb:
        size_mb = path.stat().st_size / (1024 * 1024)
        if size_mb > max_size_mb:
            raise ValueError(
                f"File exceeds maximum size of {max_size_mb}MB: {size_mb:.2f}MB"
            )

    return path


def list_files(
    directory: str, readonly_dir: str = "/data/input", pattern: str = "*"
) -> list[Path]:
    """
    Safely list files inside a directory.

    Args:
        directory: Directory to list (relative to readonly_dir).
        readonly_dir: Base read-only directory.
        pattern: Glob pattern to filter files.

    Returns:
        Sorted list of Path objects matching the pattern.

    Raises:
        PathValidationError: If the resolved directory is outside the allowed area.
    """
    try:
        allowed = Path(readonly_dir).resolve(strict=True)
        target_dir = (allowed / directory).resolve()
    except (OSError, RuntimeError) as e:
        raise PathValidationError(f"Invalid directory: {directory}") from e

    if not target_dir.is_relative_to(allowed):
        raise PathValidationError(f"Directory outside readonly area: {directory}")

    if not target_dir.is_dir():
        raise PathValidationError(f"Not a directory: {directory}")

    return sorted(target_dir.glob(pattern))
