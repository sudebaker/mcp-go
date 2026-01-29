#!/usr/bin/env python3
"""
Knowledge Base Tool - Memory System.
Manages content memorization and semantic search using PostgreSQL with pgvector.

Security Features:
- SQL injection prevention via parameterized queries
- Resource limits (content size, chunks, query length)
- Input validation and sanitization
- Secure database connection pooling
"""

import json
import os
import sys
import hashlib
import re
from typing import Any, Optional
from dataclasses import dataclass

# Add the tools directory to the path so we can import common modules
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger

# Import local modules with fallback for direct execution
try:
    from .db_pool import init_pool, get_connection, close_pool
    from .model_cache import get_embedding_model
except ImportError:
    # If running as script directly, use absolute imports
    from db_pool import init_pool, get_connection, close_pool
    from model_cache import get_embedding_model

# Initialize logger
logger = get_logger(__name__, "knowledge_base")

import psycopg2
from psycopg2.extras import execute_values

try:
    PSYCOPG2_AVAILABLE = True
except ImportError:
    PSYCOPG2_AVAILABLE = False

from sentence_transformers import SentenceTransformer

try:
    SENTENCE_TRANSFORMERS_AVAILABLE = True
except ImportError:
    SENTENCE_TRANSFORMERS_AVAILABLE = False


# Configuration
EMBEDDING_MODEL = "all-MiniLM-L6-v2"
EMBEDDING_DIM = 384
CHUNK_SIZE = 500
CHUNK_OVERLAP = 50

# Security Limits
MAX_CONTENT_SIZE_MB = int(os.getenv("MAX_CONTENT_SIZE_MB", "10"))
MAX_CHUNKS_PER_CONTENT = int(os.getenv("MAX_CHUNKS_PER_CONTENT", "1000"))
MAX_QUERY_LENGTH = int(os.getenv("MAX_QUERY_LENGTH", "1000"))
MAX_COLLECTION_NAME_LENGTH = 100
MAX_TOP_K = 100
MIN_TOP_K = 1

# Regex patterns for input validation
COLLECTION_NAME_PATTERN = re.compile(r"^[a-zA-Z0-9_-]{1,100}$")
# Allow common search characters for natural language queries
# Permits: letters, numbers, spaces, punctuation, symbols commonly used in search
SAFE_QUERY_PATTERN = re.compile(
    r"^[\w\s\-.,!?;:()'\"&/\\%#@+=\$\[\]<>~`|]+$", re.UNICODE
)


@dataclass
class DocumentChunk:
    """Represents a chunk of a document."""

    content: str
    metadata: dict[str, Any]
    embedding: list[float] | None = None


def read_request() -> dict[str, Any]:
    """Read JSON request from STDIN."""
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to STDOUT."""
    print(json.dumps(response))


def validate_collection_name(collection: str) -> tuple[bool, Optional[str]]:
    """
    Validate collection name to prevent SQL injection and other attacks.

    Returns:
        (is_valid, error_message) tuple
    """
    if not collection or not isinstance(collection, str):
        return False, "collection must be a non-empty string"

    if len(collection) > MAX_COLLECTION_NAME_LENGTH:
        return (
            False,
            f"collection name exceeds maximum length of {MAX_COLLECTION_NAME_LENGTH}",
        )

    if not COLLECTION_NAME_PATTERN.match(collection):
        return (
            False,
            "collection name must contain only alphanumeric, underscore, and hyphen characters",
        )

    return True, None


def validate_query(query: str) -> tuple[bool, Optional[str]]:
    """
    Validate search query to prevent injection attacks.

    Returns:
        (is_valid, error_message) tuple
    """
    if not query or not isinstance(query, str):
        return False, "query must be a non-empty string"

    if len(query) > MAX_QUERY_LENGTH:
        return False, f"query exceeds maximum length of {MAX_QUERY_LENGTH} characters"

    # Basic sanitization - allow common search characters
    if not SAFE_QUERY_PATTERN.match(query):
        return False, "query contains invalid characters"

    return True, None


def validate_top_k(top_k: Any) -> tuple[bool, Optional[str], int]:
    """
    Validate and sanitize top_k parameter.

    Returns:
        (is_valid, error_message, sanitized_value) tuple
    """
    try:
        top_k_int = int(top_k)
    except (TypeError, ValueError):
        return False, "top_k must be an integer", 0

    if top_k_int < MIN_TOP_K or top_k_int > MAX_TOP_K:
        return False, f"top_k must be between {MIN_TOP_K} and {MAX_TOP_K}", 0

    return True, None, top_k_int


def validate_ingest_request(
    content: str, collection: str, metadata: Any
) -> tuple[bool, Optional[str]]:
    """
    Validate ingestion request parameters.

    Returns:
        (is_valid, error_message) tuple
    """
    # Content must be provided
    if not content:
        return False, "content must be provided"

    # Validate content
    if not isinstance(content, str):
        return False, "content must be a string"

    # SECURITY: Check content size
    content_size_mb = len(content.encode("utf-8")) / (1024 * 1024)
    if content_size_mb > MAX_CONTENT_SIZE_MB:
        return (
            False,
            f"content size ({content_size_mb:.2f}MB) exceeds limit of {MAX_CONTENT_SIZE_MB}MB",
        )

    # Validate collection
    is_valid, error = validate_collection_name(collection)
    if not is_valid:
        return False, error

    # Validate metadata
    if metadata is not None and not isinstance(metadata, dict):
        return False, "metadata must be a dictionary or null"

    return True, None


def validate_search_request(
    query: str, collection: str, top_k: Any, search_type: str
) -> tuple[bool, Optional[str], int]:
    """
    Validate search request parameters.

    Returns:
        (is_valid, error_message, sanitized_top_k) tuple
    """
    # Validate query
    is_valid, error = validate_query(query)
    if not is_valid:
        return False, error, 0

    # Validate collection
    is_valid, error = validate_collection_name(collection)
    if not is_valid:
        return False, error, 0

    # Validate top_k
    is_valid, error, top_k_sanitized = validate_top_k(top_k)
    if not is_valid:
        return False, error, 0

    # Validate search_type
    valid_search_types = {"semantic", "keyword", "hybrid"}
    if search_type not in valid_search_types:
        return False, f"search_type must be one of: {', '.join(valid_search_types)}", 0

    return True, None, top_k_sanitized


def ensure_schema(conn) -> None:
    """
    Ensure the required database schema exists.

    Security: Uses parameterized queries and validated constants to prevent SQL injection.
    """
    with conn.cursor() as cur:
        # Enable pgvector extension
        cur.execute("CREATE EXTENSION IF NOT EXISTS vector")

        # Create documents table
        cur.execute("""
            CREATE TABLE IF NOT EXISTS kb_documents (
                id SERIAL PRIMARY KEY,
                doc_hash VARCHAR(64) UNIQUE NOT NULL,
                file_path TEXT NOT NULL,
                collection VARCHAR(255) NOT NULL DEFAULT 'default',
                metadata JSONB,
                created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
            )
        """)

        # Create chunks table with vector column
        # SECURITY: EMBEDDING_DIM is a validated constant, safe to interpolate
        if (
            not isinstance(EMBEDDING_DIM, int)
            or EMBEDDING_DIM <= 0
            or EMBEDDING_DIM > 10000
        ):
            raise ValueError(f"Invalid EMBEDDING_DIM: {EMBEDDING_DIM}")

        cur.execute(f"""
            CREATE TABLE IF NOT EXISTS kb_chunks (
                id SERIAL PRIMARY KEY,
                document_id INTEGER REFERENCES kb_documents(id) ON DELETE CASCADE,
                content TEXT NOT NULL,
                chunk_index INTEGER NOT NULL,
                embedding vector({EMBEDDING_DIM}),
                metadata JSONB,
                created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
            )
        """)

        # Create indexes
        cur.execute("""
            CREATE INDEX IF NOT EXISTS idx_kb_chunks_embedding
            ON kb_chunks USING ivfflat (embedding vector_cosine_ops)
            WITH (lists = 100)
        """)
        cur.execute("""
            CREATE INDEX IF NOT EXISTS idx_kb_documents_collection
            ON kb_documents(collection)
        """)
        cur.execute("""
            CREATE INDEX IF NOT EXISTS idx_kb_chunks_content_tsvector
            ON kb_chunks USING gin(to_tsvector('english', content))
        """)


def chunk_text(
    text: str, chunk_size: int = CHUNK_SIZE, overlap: int = CHUNK_OVERLAP
) -> list[str]:
    """
    Split text into overlapping chunks with resource limits.

    Security: Limits maximum number of chunks to prevent memory exhaustion.

    Raises:
        ValueError: If text would produce too many chunks
    """
    if len(text) <= chunk_size:
        return [text]

    # SECURITY: Estimate max chunks and enforce limit
    estimated_chunks = (len(text) // (chunk_size - overlap)) + 1
    if estimated_chunks > MAX_CHUNKS_PER_CONTENT:
        raise ValueError(
            f"Document would produce {estimated_chunks} chunks, "
            f"exceeding limit of {MAX_CHUNKS_PER_CONTENT}. "
            f"Consider increasing chunk_size or reducing document size."
        )

    chunks = []
    start = 0
    while start < len(text):
        # SECURITY: Double-check chunk count during processing
        if len(chunks) >= MAX_CHUNKS_PER_CONTENT:
            logger.warning(
                "Chunk limit reached during processing",
                extra_data={
                    "chunks_created": len(chunks),
                    "limit": MAX_CHUNKS_PER_CONTENT,
                },
            )
            break

        end = start + chunk_size

        # Try to break at sentence boundary
        if end < len(text):
            # Look for sentence end markers
            for marker in [". ", ".\n", "? ", "?\n", "! ", "!\n"]:
                last_marker = text[start:end].rfind(marker)
                if last_marker > chunk_size // 2:
                    end = start + last_marker + len(marker)
                    break

        chunk = text[start:end].strip()
        if chunk:
            chunks.append(chunk)
        start = end - overlap

    return chunks


def compute_doc_hash(content: str) -> str:
    """
    Compute hash for content deduplication.

    Args:
        content: Text content

    Returns:
        SHA256 hash string
    """
    return hashlib.sha256(content.encode()).hexdigest()


def ingest_document(
    conn,
    model: SentenceTransformer,
    content: str,
    collection: str,
    metadata: dict[str, Any] | None,
) -> dict[str, Any]:
    """
    Ingest content into the knowledge base.

    Security: Uses size limits and content validation.

    Args:
        conn: Database connection
        model: Embedding model
        content: Text content to ingest
        collection: Collection name (validated)
        metadata: Optional metadata

    Returns:
        Ingestion result dictionary

    Raises:
        ValueError: If validation fails or limits exceeded
    """
    # Generate source identifier from content hash
    source_identifier = f"memorized_{hashlib.sha256(content.encode()).hexdigest()[:16]}"

    # Compute document hash for deduplication
    doc_hash = compute_doc_hash(content)

    # Check if document already exists
    with conn.cursor() as cur:
        cur.execute("SELECT id FROM kb_documents WHERE doc_hash = %s", (doc_hash,))
        existing = cur.fetchone()
        if existing:
            return {
                "status": "skipped",
                "reason": "Content already memorized",
                "document_id": existing[0],
            }

    # Chunk the document
    chunks = chunk_text(content)

    # Generate embeddings
    embeddings = model.encode(chunks, show_progress_bar=False)

    # Insert document and chunks
    with conn.cursor() as cur:
        # Insert document
        cur.execute(
            """
            INSERT INTO kb_documents (doc_hash, file_path, collection, metadata)
            VALUES (%s, %s, %s, %s)
            RETURNING id
            """,
            (doc_hash, source_identifier, collection, json.dumps(metadata or {})),
        )
        doc_id = cur.fetchone()[0]

        # Insert chunks with embeddings
        chunk_data = [
            (doc_id, chunk, i, embedding.tolist(), json.dumps({"chunk_index": i}))
            for i, (chunk, embedding) in enumerate(zip(chunks, embeddings))
        ]
        execute_values(
            cur,
            """
            INSERT INTO kb_chunks (document_id, content, chunk_index, embedding, metadata)
            VALUES %s
            """,
            chunk_data,
            template="(%s, %s, %s, %s::vector, %s)",
        )

    return {
        "status": "ingested",
        "document_id": doc_id,
        "chunks_count": len(chunks),
        "source_identifier": source_identifier,
        "collection": collection,
    }


def search_semantic(
    conn, model: SentenceTransformer, query: str, collection: str, top_k: int
) -> list[dict[str, Any]]:
    """Perform semantic search using vector similarity."""
    query_embedding = model.encode([query], show_progress_bar=False)[0]

    with conn.cursor() as cur:
        cur.execute(
            """
            SELECT
                c.content,
                c.metadata,
                d.file_path,
                d.collection,
                1 - (c.embedding <=> %s::vector) as similarity
            FROM kb_chunks c
            JOIN kb_documents d ON c.document_id = d.id
            WHERE d.collection = %s
            ORDER BY c.embedding <=> %s::vector
            LIMIT %s
            """,
            (query_embedding.tolist(), collection, query_embedding.tolist(), top_k),
        )

        results = []
        for row in cur.fetchall():
            results.append(
                {
                    "content": row[0],
                    "metadata": row[1],
                    "file_path": row[2],
                    "collection": row[3],
                    "score": float(row[4]),
                }
            )

        return results


def search_keyword(
    conn, query: str, collection: str, top_k: int
) -> list[dict[str, Any]]:
    """Perform keyword search using PostgreSQL full-text search."""
    with conn.cursor() as cur:
        cur.execute(
            """
            SELECT
                c.content,
                c.metadata,
                d.file_path,
                d.collection,
                ts_rank(to_tsvector('english', c.content), plainto_tsquery('english', %s)) as rank
            FROM kb_chunks c
            JOIN kb_documents d ON c.document_id = d.id
            WHERE d.collection = %s
              AND to_tsvector('english', c.content) @@ plainto_tsquery('english', %s)
            ORDER BY rank DESC
            LIMIT %s
            """,
            (query, collection, query, top_k),
        )

        results = []
        for row in cur.fetchall():
            results.append(
                {
                    "content": row[0],
                    "metadata": row[1],
                    "file_path": row[2],
                    "collection": row[3],
                    "score": float(row[4]),
                }
            )

        return results


def search_hybrid(
    conn, model: SentenceTransformer, query: str, collection: str, top_k: int
) -> list[dict[str, Any]]:
    """Perform hybrid search combining semantic and keyword search."""
    # Get results from both methods
    semantic_results = search_semantic(conn, model, query, collection, top_k * 2)
    keyword_results = search_keyword(conn, query, collection, top_k * 2)

    # Combine and deduplicate results
    seen = set()
    combined = []

    # Weighted combination (0.7 semantic, 0.3 keyword)
    for result in semantic_results:
        key = (result["file_path"], result["content"][:100])
        if key not in seen:
            seen.add(key)
            result["search_type"] = "semantic"
            combined.append(result)

    for result in keyword_results:
        key = (result["file_path"], result["content"][:100])
        if key not in seen:
            seen.add(key)
            result["search_type"] = "keyword"
            result["score"] *= 0.8  # Slightly lower weight for keyword-only
            combined.append(result)

    # Sort by score and return top_k
    combined.sort(key=lambda x: x["score"], reverse=True)
    return combined[:top_k]


def handle_ingest(request: dict[str, Any], context: dict[str, Any]) -> dict[str, Any]:
    """
    Handle content ingestion with comprehensive validation.

    Security: Validates all inputs before processing.
    """
    arguments = request.get("arguments", {})
    content = arguments.get("content", "")
    collection = arguments.get("collection", "default")
    metadata = arguments.get("metadata")

    # SECURITY: Validate all inputs
    is_valid, error_msg = validate_ingest_request(content, collection, metadata)
    if not is_valid:
        logger.warning("Invalid ingest request", extra_data={"error": error_msg})
        return {
            "success": False,
            "error": {"code": "INVALID_INPUT", "message": error_msg},
        }

    database_url = context.get("database_url")
    if not database_url:
        return {
            "success": False,
            "error": {
                "code": "DATABASE_ERROR",
                "message": "DATABASE_URL not configured",
            },
        }

    # Initialize connection pool if not already done
    init_pool(database_url)

    # Load embedding model (cached)
    model = get_embedding_model(EMBEDDING_MODEL)

    # Use connection from pool
    try:
        with get_connection() as conn:
            ensure_schema(conn)
            result = ingest_document(conn, model, content, collection, metadata)

            logger.info(
                "Content memorized successfully",
                extra_data={
                    "collection": collection,
                    "status": result["status"],
                    "chunks_count": result.get("chunks_count", 0),
                },
            )

            return {
                "success": True,
                "content": [
                    {"type": "text", "text": f"Content memorized: {result['status']}"}
                ],
                "structured_content": result,
            }
    except ValueError as e:
        logger.error("Validation error during ingestion", extra_data={"error": str(e)})
        return {
            "success": False,
            "error": {"code": "VALIDATION_ERROR", "message": str(e)},
        }
    except Exception as e:
        logger.error("Unexpected error during ingestion", extra_data={"error": str(e)})
        raise


def handle_search(request: dict[str, Any], context: dict[str, Any]) -> dict[str, Any]:
    """
    Handle knowledge base search with comprehensive validation.

    Security: Validates all inputs and sanitizes top_k parameter.
    """
    arguments = request.get("arguments", {})
    query = arguments.get("query", "")
    collection = arguments.get("collection", "default")
    top_k = arguments.get("top_k", 5)
    search_type = arguments.get("search_type", "hybrid")

    # SECURITY: Validate all inputs
    is_valid, error_msg, top_k_sanitized = validate_search_request(
        query, collection, top_k, search_type
    )
    if not is_valid:
        logger.warning("Invalid search request", extra_data={"error": error_msg})
        return {
            "success": False,
            "error": {"code": "INVALID_INPUT", "message": error_msg},
        }

    database_url = context.get("database_url")
    if not database_url:
        return {
            "success": False,
            "error": {
                "code": "DATABASE_ERROR",
                "message": "DATABASE_URL not configured",
            },
        }

    # Initialize connection pool if not already done
    init_pool(database_url)

    # Load embedding model (needed for semantic search, cached)
    model = None
    if search_type in ("semantic", "hybrid"):
        model = get_embedding_model(EMBEDDING_MODEL)

    # Use connection from pool
    try:
        with get_connection() as conn:
            ensure_schema(conn)

            # Use sanitized top_k value
            if search_type == "semantic":
                results = search_semantic(
                    conn, model, query, collection, top_k_sanitized
                )
            elif search_type == "keyword":
                results = search_keyword(conn, query, collection, top_k_sanitized)
            else:  # hybrid
                results = search_hybrid(conn, model, query, collection, top_k_sanitized)

            logger.info(
                "Search completed",
                extra_data={
                    "query_length": len(query),
                    "collection": collection,
                    "search_type": search_type,
                    "results_count": len(results),
                },
            )

            # Format results for output
            output_text = f"Found {len(results)} results for: {query}\n\n"
            for i, result in enumerate(results, 1):
                output_text += f"--- Result {i} (score: {result['score']:.3f}) ---\n"
                output_text += f"Source: {result['file_path']}\n"
                # Limit content preview to 500 chars
                content_preview = result["content"][:500]
                if len(result["content"]) > 500:
                    content_preview += "..."
                output_text += f"{content_preview}\n\n"

            return {
                "success": True,
                "content": [{"type": "text", "text": output_text}],
                "structured_content": {
                    "query": query,
                    "collection": collection,
                    "search_type": search_type,
                    "results_count": len(results),
                    "results": results,
                },
            }
    except Exception as e:
        logger.error("Error during search", extra_data={"error": str(e)})
        raise


def main() -> None:
    """Main entry point with comprehensive error handling."""
    request = {}

    # Check dependencies
    if not PSYCOPG2_AVAILABLE:
        write_response(
            {
                "success": False,
                "request_id": "",
                "error": {
                    "code": "DEPENDENCY_MISSING",
                    "message": "psycopg2 is required. Install with: pip install psycopg2-binary",
                },
            }
        )
        return

    if not SENTENCE_TRANSFORMERS_AVAILABLE:
        write_response(
            {
                "success": False,
                "request_id": "",
                "error": {
                    "code": "DEPENDENCY_MISSING",
                    "message": "sentence-transformers is required. Install with: pip install sentence-transformers",
                },
            }
        )
        return

    try:
        request = read_request()
        request_id = request.get("request_id", "")
        context = request.get("context", {})

        # Determine operation from command line args
        operation = sys.argv[1] if len(sys.argv) > 1 else "search"

        # SECURITY: Validate operation
        valid_operations = {"ingest", "search"}
        if operation not in valid_operations:
            logger.warning(
                "Invalid operation requested", extra_data={"operation": operation}
            )
            result = {
                "success": False,
                "error": {
                    "code": "INVALID_INPUT",
                    "message": f"Unknown operation: {operation}. Valid: {', '.join(valid_operations)}",
                },
            }
        elif operation == "ingest":
            result = handle_ingest(request, context)
        else:  # search
            result = handle_search(request, context)

        result["request_id"] = request_id
        write_response(result)

    except ValueError as e:
        # Validation errors
        logger.error("Validation error", extra_data={"error": str(e)})
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", ""),
                "error": {
                    "code": "VALIDATION_ERROR",
                    "message": str(e),
                },
            }
        )
    except PermissionError as e:
        # File access errors
        logger.error("Permission denied", extra_data={"error": str(e)})
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", ""),
                "error": {
                    "code": "PERMISSION_DENIED",
                    "message": str(e),
                },
            }
        )
    except Exception as e:
        # Unexpected errors
        logger.error(
            "Unexpected error", extra_data={"error": str(e), "type": type(e).__name__}
        )
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", ""),
                "error": {
                    "code": "EXECUTION_FAILED",
                    "message": str(e),
                    "details": str(type(e).__name__),
                },
            }
        )
    finally:
        # Cleanup connection pool
        try:
            close_pool()
        except Exception as e:
            logger.warning(
                "Error closing connection pool", extra_data={"error": str(e)}
            )


if __name__ == "__main__":
    main()
