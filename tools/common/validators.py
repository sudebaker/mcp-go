from pathlib import Path
import os
from typing import Optional


class PathValidationError(ValueError):
    """Exception raised for path validation errors."""

    pass


def validate_file_path(file_path: str, allowed_dir: str = "/data") -> Path:
    """
    Valida que el archivo esté dentro del directorio permitido de forma segura.

    Args:
        file_path: Ruta del archivo a validar
        allowed_dir: Directorio base permitido

    Returns:
        Path: Ruta validada y resuelta

    Raises:
        PathValidationError: Si se detecta path traversal
        FileNotFoundError: Si el archivo no existe
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
    Valida que la ruta de salida esté dentro del directorio permitido.
    Crea directorios padres si no existen.

    Args:
        output_path: Ruta de salida a validar
        allowed_dir: Directorio base permitido
        check_writable: Si debe verificar permisos de escritura

    Returns:
        Path: Ruta validada

    Raises:
        PathValidationError: Si se detecta path traversal
        PermissionError: Si no hay permisos de escritura
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
    Sanitiza un nombre de archivo removiendo caracteres peligrosos.

    Args:
        filename: Nombre de archivo a sanitizar
        max_length: Longitud máxima permitida

    Returns:
        str: Nombre de archivo sanitizado

    Raises:
        ValueError: Si el nombre está vacío después de sanitizar
    """
    import re

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
    Valida un path para operaciones de LECTURA únicamente.

    Args:
        file_path: Ruta del archivo a leer
        readonly_dir: Directorio base de solo lectura

    Returns:
        Path: Ruta validada y resuelta

    Raises:
        PathValidationError: Si se detecta path traversal
        FileNotFoundError: Si el archivo no existe
        PermissionError: Si el archivo no es legible
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
    Valida un path para operaciones de ESCRITURA.

    Args:
        file_path: Ruta del archivo a escribir
        writable_dir: Directorio base de escritura
        create_parents: Si crear directorios padres
        max_size_mb: Tamaño máximo permitido en MB

    Returns:
        Path: Ruta validada

    Raises:
        PathValidationError: Si se detecta path traversal
        PermissionError: Si no hay permisos de escritura
        ValueError: Si el archivo excede tamaño máximo
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
    Lista archivos en un directorio de forma segura.

    Args:
        directory: Directorio a listar (relativo a readonly_dir)
        readonly_dir: Directorio base de solo lectura
        pattern: Patrón glob para filtrar archivos

    Returns:
        list[Path]: Lista de rutas de archivos

    Raises:
        PathValidationError: Si el directorio está fuera del allowed
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
