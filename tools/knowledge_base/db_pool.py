"""
Database connection pool with security hardening.

Security Features:
- Connection pool size limits
- Connection timeout enforcement
- Proper error handling and cleanup
- Thread-safe operations
"""

import os
import sys
from psycopg2 import pool
from contextlib import contextmanager

# Add parent directory to path for common imports
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from common.structured_logging import get_logger

logger = get_logger(__name__, "db_pool")

_connection_pool = None

# Security: Enforce reasonable pool size limits
MIN_POOL_SIZE = 1
MAX_POOL_SIZE = 50
DEFAULT_MIN_CONN = 2
DEFAULT_MAX_CONN = 10
CONNECTION_TIMEOUT = 30  # seconds


def validate_pool_params(minconn: int, maxconn: int) -> tuple[bool, str, int, int]:
    """
    Validate and sanitize connection pool parameters.

    Returns:
        (is_valid, error_message, sanitized_minconn, sanitized_maxconn) tuple
    """
    try:
        minconn_int = int(minconn)
        maxconn_int = int(maxconn)
    except (TypeError, ValueError):
        return False, "Pool size parameters must be integers", 0, 0

    if minconn_int < MIN_POOL_SIZE or minconn_int > MAX_POOL_SIZE:
        return (
            False,
            f"minconn must be between {MIN_POOL_SIZE} and {MAX_POOL_SIZE}",
            0,
            0,
        )

    if maxconn_int < MIN_POOL_SIZE or maxconn_int > MAX_POOL_SIZE:
        return (
            False,
            f"maxconn must be between {MIN_POOL_SIZE} and {MAX_POOL_SIZE}",
            0,
            0,
        )

    if minconn_int > maxconn_int:
        return False, "minconn cannot be greater than maxconn", 0, 0

    return True, "", minconn_int, maxconn_int


def init_pool(
    database_url: str, minconn: int | None = None, maxconn: int | None = None
):
    """
    Initialize database connection pool with security validation.

    Args:
        database_url: PostgreSQL connection string
        minconn: Minimum connections (default: 2, range: 1-50)
        maxconn: Maximum connections (default: 10, range: 1-50)

    Raises:
        ValueError: If pool parameters are invalid
        RuntimeError: If pool initialization fails
    """
    global _connection_pool
    if _connection_pool is not None:
        logger.info("Connection pool already initialized")
        return  # Already initialized

    # Get pool size from environment or use defaults
    if minconn is None:
        minconn = int(os.getenv("DB_POOL_MIN", str(DEFAULT_MIN_CONN)))
    if maxconn is None:
        maxconn = int(os.getenv("DB_POOL_MAX", str(DEFAULT_MAX_CONN)))

    # SECURITY: Validate pool parameters
    is_valid, error_msg, minconn_safe, maxconn_safe = validate_pool_params(
        minconn, maxconn
    )
    if not is_valid:
        logger.error("Invalid pool parameters", extra={"error": error_msg})
        raise ValueError(f"Invalid pool parameters: {error_msg}")

    # SECURITY: Validate database URL format
    if not database_url or not isinstance(database_url, str):
        raise ValueError("database_url must be a non-empty string")

    if not database_url.startswith(("postgresql://", "postgres://")):
        raise ValueError("database_url must start with postgresql:// or postgres://")

    try:
        _connection_pool = pool.ThreadedConnectionPool(
            minconn=minconn_safe,
            maxconn=maxconn_safe,
            dsn=database_url,
            connect_timeout=CONNECTION_TIMEOUT,
        )
        logger.info(
            "Connection pool initialized",
            extra={"minconn": minconn_safe, "maxconn": maxconn_safe},
        )
    except Exception as e:
        logger.error("Failed to initialize connection pool", extra={"error": str(e)})
        raise RuntimeError(f"Failed to initialize connection pool: {str(e)}") from e


@contextmanager
def get_connection():
    """
    Get a connection from the pool with proper error handling.

    Yields:
        Database connection

    Raises:
        RuntimeError: If pool is not initialized
    """
    if _connection_pool is None:
        raise RuntimeError("Connection pool not initialized. Call init_pool() first.")

    conn = None
    try:
        conn = _connection_pool.getconn()
        if conn is None:
            raise RuntimeError("Failed to get connection from pool")

        yield conn
        conn.commit()

    except Exception as e:
        if conn is not None:
            try:
                conn.rollback()
                logger.warning(
                    "Transaction rolled back due to error", extra={"error": str(e)}
                )
            except Exception as rollback_error:
                logger.error(
                    "Failed to rollback transaction",
                    extra={"error": str(rollback_error)},
                )
        raise

    finally:
        if conn is not None:
            try:
                _connection_pool.putconn(conn)
            except Exception as e:
                logger.error(
                    "Failed to return connection to pool", extra={"error": str(e)}
                )


def close_pool():
    """
    Close all connections in the pool and cleanup.

    This should be called when shutting down the application.
    """
    global _connection_pool
    if _connection_pool:
        try:
            _connection_pool.closeall()
            logger.info("Connection pool closed successfully")
        except Exception as e:
            logger.error("Error closing connection pool", extra={"error": str(e)})
        finally:
            _connection_pool = None
