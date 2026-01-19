#!/usr/bin/env python3
"""
Knowledge Base Tool.
Manages document ingestion and semantic search using PostgreSQL with pgvector.
"""

import json
import os
import sys
import hashlib
from pathlib import Path
from typing import Any
from dataclasses import dataclass

try:
    import psycopg2
    from psycopg2.extras import execute_values
    PSYCOPG2_AVAILABLE = True
except ImportError:
    PSYCOPG2_AVAILABLE = False

try:
    from sentence_transformers import SentenceTransformer
    SENTENCE_TRANSFORMERS_AVAILABLE = True
except ImportError:
    SENTENCE_TRANSFORMERS_AVAILABLE = False


# Configuration
EMBEDDING_MODEL = "all-MiniLM-L6-v2"
EMBEDDING_DIM = 384
CHUNK_SIZE = 500
CHUNK_OVERLAP = 50


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


def get_db_connection(database_url: str):
    """Create database connection."""
    return psycopg2.connect(database_url)


def ensure_schema(conn) -> None:
    """Ensure the required database schema exists."""
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

        conn.commit()


def chunk_text(text: str, chunk_size: int = CHUNK_SIZE, overlap: int = CHUNK_OVERLAP) -> list[str]:
    """Split text into overlapping chunks."""
    if len(text) <= chunk_size:
        return [text]

    chunks = []
    start = 0
    while start < len(text):
        end = start + chunk_size

        # Try to break at sentence boundary
        if end < len(text):
            # Look for sentence end markers
            for marker in ['. ', '.\n', '? ', '?\n', '! ', '!\n']:
                last_marker = text[start:end].rfind(marker)
                if last_marker > chunk_size // 2:
                    end = start + last_marker + len(marker)
                    break

        chunks.append(text[start:end].strip())
        start = end - overlap

    return [c for c in chunks if c]


def read_document(file_path: Path) -> str:
    """Read document content based on file type."""
    suffix = file_path.suffix.lower()

    if suffix == '.txt':
        return file_path.read_text(encoding='utf-8')

    elif suffix == '.md':
        return file_path.read_text(encoding='utf-8')

    elif suffix == '.pdf':
        try:
            import pypdf
            reader = pypdf.PdfReader(str(file_path))
            text = []
            for page in reader.pages:
                text.append(page.extract_text())
            return '\n'.join(text)
        except ImportError:
            raise ImportError("pypdf is required for PDF files. Install with: pip install pypdf")

    elif suffix == '.docx':
        try:
            from docx import Document
            doc = Document(str(file_path))
            return '\n'.join([para.text for para in doc.paragraphs])
        except ImportError:
            raise ImportError("python-docx is required for DOCX files. Install with: pip install python-docx")

    else:
        # Try to read as text
        return file_path.read_text(encoding='utf-8')


def compute_doc_hash(content: str, file_path: str) -> str:
    """Compute hash for document deduplication."""
    return hashlib.sha256(f"{file_path}:{content}".encode()).hexdigest()


def ingest_document(
    conn,
    model: SentenceTransformer,
    file_path: str,
    collection: str,
    metadata: dict[str, Any] | None
) -> dict[str, Any]:
    """Ingest a document into the knowledge base."""
    path = Path(file_path)
    if not path.exists():
        raise FileNotFoundError(f"File not found: {file_path}")

    # Read document
    content = read_document(path)
    doc_hash = compute_doc_hash(content, file_path)

    # Check if document already exists
    with conn.cursor() as cur:
        cur.execute("SELECT id FROM kb_documents WHERE doc_hash = %s", (doc_hash,))
        existing = cur.fetchone()
        if existing:
            return {
                "status": "skipped",
                "reason": "Document already exists",
                "document_id": existing[0]
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
            (doc_hash, file_path, collection, json.dumps(metadata or {}))
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
            template="(%s, %s, %s, %s::vector, %s)"
        )

        conn.commit()

    return {
        "status": "ingested",
        "document_id": doc_id,
        "chunks_count": len(chunks),
        "file_path": file_path,
        "collection": collection
    }


def search_semantic(
    conn,
    model: SentenceTransformer,
    query: str,
    collection: str,
    top_k: int
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
            (query_embedding.tolist(), collection, query_embedding.tolist(), top_k)
        )

        results = []
        for row in cur.fetchall():
            results.append({
                "content": row[0],
                "metadata": row[1],
                "file_path": row[2],
                "collection": row[3],
                "score": float(row[4])
            })

        return results


def search_keyword(
    conn,
    query: str,
    collection: str,
    top_k: int
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
            (query, collection, query, top_k)
        )

        results = []
        for row in cur.fetchall():
            results.append({
                "content": row[0],
                "metadata": row[1],
                "file_path": row[2],
                "collection": row[3],
                "score": float(row[4])
            })

        return results


def search_hybrid(
    conn,
    model: SentenceTransformer,
    query: str,
    collection: str,
    top_k: int
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
    """Handle document ingestion."""
    arguments = request.get("arguments", {})
    file_path = arguments.get("file_path")
    collection = arguments.get("collection", "default")
    metadata = arguments.get("metadata")

    if not file_path:
        return {
            "success": False,
            "error": {
                "code": "INVALID_INPUT",
                "message": "file_path is required"
            }
        }

    database_url = context.get("database_url")
    if not database_url:
        return {
            "success": False,
            "error": {
                "code": "DATABASE_ERROR",
                "message": "DATABASE_URL not configured"
            }
        }

    # Load embedding model
    model = SentenceTransformer(EMBEDDING_MODEL)

    # Connect to database
    conn = get_db_connection(database_url)
    try:
        ensure_schema(conn)
        result = ingest_document(conn, model, file_path, collection, metadata)

        return {
            "success": True,
            "content": [{"type": "text", "text": f"Document ingested: {result['status']}"}],
            "structured_content": result
        }
    finally:
        conn.close()


def handle_search(request: dict[str, Any], context: dict[str, Any]) -> dict[str, Any]:
    """Handle knowledge base search."""
    arguments = request.get("arguments", {})
    query = arguments.get("query")
    collection = arguments.get("collection", "default")
    top_k = arguments.get("top_k", 5)
    search_type = arguments.get("search_type", "hybrid")

    if not query:
        return {
            "success": False,
            "error": {
                "code": "INVALID_INPUT",
                "message": "query is required"
            }
        }

    database_url = context.get("database_url")
    if not database_url:
        return {
            "success": False,
            "error": {
                "code": "DATABASE_ERROR",
                "message": "DATABASE_URL not configured"
            }
        }

    # Load embedding model (needed for semantic search)
    model = None
    if search_type in ("semantic", "hybrid"):
        model = SentenceTransformer(EMBEDDING_MODEL)

    # Connect to database
    conn = get_db_connection(database_url)
    try:
        ensure_schema(conn)

        if search_type == "semantic":
            results = search_semantic(conn, model, query, collection, top_k)
        elif search_type == "keyword":
            results = search_keyword(conn, query, collection, top_k)
        else:  # hybrid
            results = search_hybrid(conn, model, query, collection, top_k)

        # Format results for output
        output_text = f"Found {len(results)} results for: {query}\n\n"
        for i, result in enumerate(results, 1):
            output_text += f"--- Result {i} (score: {result['score']:.3f}) ---\n"
            output_text += f"Source: {result['file_path']}\n"
            output_text += f"{result['content'][:500]}...\n\n"

        return {
            "success": True,
            "content": [{"type": "text", "text": output_text}],
            "structured_content": {
                "query": query,
                "collection": collection,
                "search_type": search_type,
                "results_count": len(results),
                "results": results
            }
        }
    finally:
        conn.close()


def main() -> None:
    # Check dependencies
    if not PSYCOPG2_AVAILABLE:
        write_response({
            "success": False,
            "request_id": "",
            "error": {
                "code": "DEPENDENCY_MISSING",
                "message": "psycopg2 is required. Install with: pip install psycopg2-binary"
            }
        })
        return

    if not SENTENCE_TRANSFORMERS_AVAILABLE:
        write_response({
            "success": False,
            "request_id": "",
            "error": {
                "code": "DEPENDENCY_MISSING",
                "message": "sentence-transformers is required. Install with: pip install sentence-transformers"
            }
        })
        return

    try:
        request = read_request()
        request_id = request.get("request_id", "")
        context = request.get("context", {})

        # Determine operation from command line args
        operation = sys.argv[1] if len(sys.argv) > 1 else "search"

        if operation == "ingest":
            result = handle_ingest(request, context)
        elif operation == "search":
            result = handle_search(request, context)
        else:
            result = {
                "success": False,
                "error": {
                    "code": "INVALID_INPUT",
                    "message": f"Unknown operation: {operation}"
                }
            }

        result["request_id"] = request_id
        write_response(result)

    except Exception as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", "") if 'request' in dir() else "",
            "error": {
                "code": "EXECUTION_FAILED",
                "message": str(e),
                "details": str(type(e).__name__)
            }
        })


if __name__ == "__main__":
    main()
