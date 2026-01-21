from pathlib import Path


def validate_file_path(file_path: str, allowed_dir: str = "/data") -> None:
    """
    Valida que el archivo esté dentro del directorio permitido.
    Lanza ValueError si intenta path traversal.
    """
    allowed = Path(allowed_dir).resolve()
    path = Path(file_path).resolve()

    if not path.is_relative_to(allowed):
        raise ValueError(f"Path traversal detected: {file_path}")

    if not path.exists():
        raise FileNotFoundError(f"File not found: {file_path}")


def validate_output_path(output_path: str, allowed_dir: str = "/data") -> None:
    """
    Valida que la ruta de salida esté dentro del directorio permitido.
    Crea directorios padres si no existen.
    Lanza ValueError si intenta path traversal.
    """
    allowed = Path(allowed_dir).resolve()
    path = Path(output_path).resolve()

    if not str(path).startswith(str(allowed)):
        raise ValueError(f"Path traversal detected: {output_path}")

    path.parent.mkdir(parents=True, exist_ok=True)
