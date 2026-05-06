# User Isolation for KB Tools

**Project:** MCP-Go Orchestrator  
**Topic:** Session/User Discrimination for kb_ingest and kb_search  
**Date:** 2026-05-06  
**Status:** Approved

---

## Overview

Implement per-user isolation for the knowledge base (`kb_ingest` and `kb_search` tools) so that users can only access their own memorized content. Users are identified via a `user_id` passed in the MCP initialize request, and this ID is stored alongside documents in PostgreSQL to filter queries.

---

## Requirements

### Functional

1. **User Isolation**: Each user can only see/access documents they inserted
2. **Transparent**: Clients don't need modification - just send `user_id` in initialize
3. **No MCP Protocol Breakage**: Use standard MCP mechanisms (`capabilities.experimental`, `Mcp-Session-Id`)
4. **Backward Compatibility**: If no `user_id` provided → fallback to `"anonymous"`

### Non-Functional

1. **Performance**: Minimal overhead - DB index on `user_id`
2. **Security**: SQL injection prevention via parameterized queries
3. **Simplicity**: Minimal code changes, no external dependencies

---

## Current State

### Before

```
kb_documents table:
  id, doc_hash, file_path, collection, metadata, created_at

kb_search: SELECT ... FROM kb_documents WHERE d.collection = ?  -- No user filter
kb_ingest: INSERT INTO kb_documents (doc_hash, ...) VALUES (...)  -- No user_id
```

**Problem**: All users share the same knowledge base. No isolation.

---

## Design

### Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                           MCP Client                            │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ POST /mcp (initialize)                                  │   │
│  │ {                                                       │   │
│  │   "capabilities": {                                     │   │
│  │     "experimental": { "user_id": "user_abc" }          │   │
│  │   }                                                     │   │
│  │ }                                                       │   │
│  └─────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
                            │
                            │ 1. Extract user_id from capabilities.experimental
                            │ 2. Generate Mcp-Session-Id
                            │ 3. Store map[session_id] = user_id
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│                          Go Server                              │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ SessionStore: sync.Map[sessionID]userID                 │   │
│  │   "sess_xyz" → "user_abc"                               │   │
│  │   "sess_abc" → "user_xyz"                               │   │
│  └─────────────────────────────────────────────────────────┘   │
│                            │                                    │
│  ┌───────────────────────▼─────────────────────────────────┐   │
│  │ On tool call (kb_ingest/kb_search):                     │   │
│  │  1. Extract session_id from header                     │   │
│  │  2. Lookup user_id from SessionStore                   │   │
│  │  3. Inject user_id into SubprocessContext              │   │
│  └─────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
                            │
                            │ 4. SubprocessContext.user_id = "user_abc"
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│                     Python Tool (knowledge_base)                │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ kb_ingest: INSERT ... (..., user_id) VALUES (..., ?)   │   │
│  │ kb_search: SELECT ... WHERE d.user_id = ?              │   │
│  └─────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│                      PostgreSQL (kb_documents)                  │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ id | doc_hash | file_path | collection | user_id | ... │   │
│  │ ...| ...      | ...       | default    | user_abc| ... │   │
│  │ ...| ...      | ...       | default    | user_xyz| ... │   │
│  └─────────────────────────────────────────────────────────┘   │
│  Index: idx_kb_documents_user_id                                │
└─────────────────────────────────────────────────────────────────┘
```

---

## Implementation Details

### 1. Database Schema Changes

**Migration:** `docs/superpowers/migrations/2026-05-06-add-user-id-to-kb-documents.sql`

```sql
-- Add user_id column to kb_documents
ALTER TABLE kb_documents 
ADD COLUMN user_id VARCHAR(255) NOT NULL DEFAULT 'anonymous';

-- Create index for efficient user-based filtering
CREATE INDEX idx_kb_documents_user_id ON kb_documents(user_id);

-- Note: kb_chunks doesn't need user_id - it references kb_documents via document_id
```

### 2. Go Server Changes

#### a. New File: `internal/session/store.go`

```go
package session

import (
    "sync"
)

// Store manages session-to-user-id mapping
type Store struct {
    mu       sync.RWMutex
    sessions map[string]string // sessionID → userID
}

// New creates a new session store
func New() *Store {
    return &Store{
        sessions: make(map[string]string),
    }
}

// Set associates a session_id with a user_id
func (s *Store) Set(sessionID, userID string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.sessions[sessionID] = userID
}

// Get retrieves the user_id for a session
func (s *Store) Get(sessionID string) (string, bool) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    userID, ok := s.sessions[sessionID]
    return userID, ok
}

// Delete removes a session from the store
func (s *Store) Delete(sessionID string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    delete(s.sessions, sessionID)
}
```

#### b. Modify `cmd/server/main.go`

Add imports and session store initialization:

```go
// Add import
import "github.com/sudebaker/mcp-go/internal/session"

// In main():
// Initialize session store
sessionStore := session.New()

// Register session hooks
mcpServer.OnAfterInitialize(func(ctx context.Context, id any, message *mcp.InitializeRequest, result *mcp.InitializeResult) {
    // Extract user_id from capabilities.experimental
    if experimental, ok := message.Params.Capabilities.Experimental["user_id"]; ok {
        if userID, ok := experimental.(string); ok {
            // Get session ID from the request
            sessionID := sessionIDFromContext(ctx)
            sessionStore.Set(sessionID, userID)
            log.Info().Str("session_id", sessionID).Str("user_id", userID).Msg("Session associated with user")
        }
    }
})

// Register session cleanup
mcpServer.OnBeforeSessionRemove(func(sessionID string) {
    sessionStore.Delete(sessionID)
    log.Info().Str("session_id", sessionID).Msg("Session removed")
})
```

#### c. Modify `internal/executor/subprocess.go`

Inject `user_id` into context for KB tools:

```go
// Add to SubprocessContext in SubprocessRequest
type SubprocessContext struct {
    // ... existing fields ...
    UserID string `json:"user_id,omitempty"`
}

// In Executor.Execute():
func (e *Executor) Execute(...) (*ExecuteResult, error) {
    // ... existing code ...
    
    // Determine if we need to inject user_id
    var userID string
    if toolName == "kb_ingest" || toolName == "kb_search" {
        // Get session ID from context (need to pass it through)
        if sid, ok := sessionIDFromContext(ctx).(string); ok {
            if uid, exists := sessionStore.Get(sid); exists {
                userID = uid
            }
        }
    }
    
    subprocReq := mcptypes.SubprocessRequest{
        RequestID: requestID,
        ToolName:  toolName,
        Arguments: arguments,
        Context: mcptypes.SubprocessContext{
            LLM_API_URL:  e.config.Execution.Environment["LLM_API_URL"],
            LLM_MODEL:    e.config.Execution.Environment["LLM_MODEL"],
            DATABASE_URL: e.config.Execution.Environment["DATABASE_URL"],
            WORKING_DIR:  e.config.Execution.WorkingDir,
            USER_ID:      userID,
        },
    }
    
    // ... rest of existing code ...
}
```

#### d. Modify `internal/mcp/types.go`

Add `UserID` field to `SubprocessContext`:

```go
type SubprocessContext struct {
    LLM_API_URL   string `json:"llm_api_url,omitempty"`
    LLM_MODEL     string `json:"llm_model,omitempty"`
    DATABASE_URL  string `json:"database_url,omitempty"`
    WORKING_DIR   string `json:"working_dir,omitempty"`
    USER_ID       string `json:"user_id,omitempty"`
}
```

### 3. Python Tool Changes

#### a. Modify `tools/knowledge_base/main.py`

**In `handle_ingest()`:**

```python
# After line 597:
result = ingest_document(conn, model, content, collection, metadata, user_id)

# Update ingest_document signature:
def ingest_document(
    conn,
    model: SentenceTransformer,
    content: str,
    collection: str,
    metadata: dict[str, Any] | None,
    user_id: str,
) -> dict[str, Any]:
    # Generate source identifier from content hash
    source_identifier = f"memorized_{hashlib.sha256((content + user_id).encode()).hexdigest()[:16]}"

    # Compute document hash for deduplication (include user_id)
    doc_hash = hashlib.sha256((content + user_id).encode()).hexdigest()
    
    # ... rest of existing code, add user_id to INSERT ...
    
    cur.execute(
        """
        INSERT INTO kb_documents (doc_hash, file_path, collection, metadata, user_id)
        VALUES (%s, %s, %s, %s, %s)
        RETURNING id
        """,
        (doc_hash, source_identifier, collection, json.dumps(metadata or {}), user_id),
    )
```

**In `handle_search()`:**

```python
# After line 673:
if search_type == "semantic":
    results = search_semantic(
        conn, model, query, collection, top_k_sanitized, user_id
    )
elif search_type == "keyword":
    results = search_keyword(conn, query, collection, top_k_sanitized, user_id)
else:  # hybrid
    results = search_hybrid(conn, model, query, collection, top_k_sanitized, user_id)
```

**Update search functions to filter by user_id:**

```python
def search_semantic(
    conn, model: SentenceTransformer, query: str, collection: str, top_k: int, user_id: str
) -> list[dict[str, Any]]:
    # ... existing code ...
    
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
        WHERE d.collection = %s AND d.user_id = %s
        ORDER BY c.embedding <=> %s::vector
        LIMIT %s
        """,
        (query_embedding.tolist(), collection, user_id, query_embedding.tolist(), top_k),
    )
    
    # ... rest of existing code ...

# Update search_keyword and search_hybrid similarly
```

---

## Testing

### Unit Tests

**Go Tests:**
- `internal/session/store_test.go` - Test SessionStore CRUD operations
- `tests/session_test.go` - End-to-end session/user isolation tests

**Python Tests:**
- `tests/tools/knowledge_base/test_user_isolation.py` - Test per-user queries

### Manual Testing

1. **Initialize with user_id:**
   ```bash
   curl -X POST http://localhost:8080/mcp \
     -H "Content-Type: application/json" \
     -d '{
       "jsonrpc": "2.0",
       "method": "initialize",
       "params": {
         "protocolVersion": "2025-03-26",
         "capabilities": {
           "experimental": {"user_id": "user_a"}
         },
         "clientInfo": {"name": "test", "version": "1.0"}
       }
     }'
   ```

2. **Ingest content:**
   ```bash
   curl -X POST http://localhost:8080/mcp \
     -H "Mcp-Session-Id: <from_above>" \
     -d '{"method": "tools/call", "params": {"name": "kb_ingest", "arguments": {"content": "Hello from user_a", "collection": "default"}}}'
   ```

3. **Verify isolation:**
   - User A searches → sees "Hello from user_a"
   - Initialize as User B → searches → does NOT see User A's content

---

## Backward Compatibility

- If no `user_id` in `capabilities.experimental`, fallback to `"anonymous"`
- Existing clients work without modification
- Database migration is backward-compatible (default value)

---

## Security Considerations

1. **SQL Injection**: Parameterized queries prevent injection in all modified functions
2. **Deduplication**: Including `user_id` in hash prevents same-content collision across users
3. **Race Conditions**: SessionStore uses `sync.RWMutex` for thread safety
4. **Memory Limits**: SessionStore entries are cleaned up on session removal

---

## Performance Impact

- **Database**: Single index on `user_id` column (~100-200 bytes per row)
- **Query Impact**: `WHERE user_id = ?` is O(log n) with index, negligible overhead
- **Go Server**: Map lookup is O(1), minimal CPU overhead

---

## Migration Path

1. ✅ Write design document (this file)
2. Run database migration
3. Deploy updated Go server
4. Update Python tools
5. Test with existing and new clients

---

## Future Enhancements

Potential improvements (out of scope for this spec):

1. **Token-based auth**: JWT in initialize to verify user identity
2. **Admin view**: Special role to view all users' content
3. **Session cleanup**: TTL-based cleanup of stale sessions
4. **Metrics**: Track per-user KB usage

---

## References

- [MCP Specification](https://modelcontextprotocol.io/specification/2025-03-26/)
- [mcp-go v0.43.2 Source](https://github.com/mark3labs/mcp-go)
- KB Memory System: `docs/KB_MEMORY_SYSTEM.md`
- Project Architecture: `README_ARCHITECTURE.md`
