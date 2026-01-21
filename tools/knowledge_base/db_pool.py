import os
from psycopg2 import pool
from contextlib import contextmanager

_connection_pool = None


def init_pool(
    database_url: str, minconn: int | None = None, maxconn: int | None = None
):
    global _connection_pool
    if _connection_pool is not None:
        return  # Already initialized

    if minconn is None:
        minconn = int(os.getenv("DB_POOL_MIN", "2"))
    if maxconn is None:
        maxconn = int(os.getenv("DB_POOL_MAX", "10"))
    global _connection_pool
    _connection_pool = pool.ThreadedConnectionPool(
        minconn=minconn, maxconn=maxconn, dsn=database_url
    )


@contextmanager
def get_connection():
    if _connection_pool is None:
        raise RuntimeError("Connection pool not initialized")
    conn = _connection_pool.getconn()
    try:
        yield conn
        conn.commit()
    finally:
        _connection_pool.putconn(conn)


def close_pool():
    global _connection_pool
    if _connection_pool:
        _connection_pool.closeall()
        _connection_pool = None
