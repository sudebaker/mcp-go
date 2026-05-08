# Path Traversal Fix - Download Handler

**Date:** 2026-05-08
**Status:** Approved
**Author:** MCP-Go Team

## Problem Statement

Clients using the PDF report generation tool cannot download PDF files due to a path traversal vulnerability in the download handler (`internal/transport/download.go`). Three bugs cause false positive path traversal detection and prevent legitimate file downloads.

## Root Cause Analysis

Three bugs identified in `internal/transport/download.go`:

| Bug | Location | Issue | Impact |
|-----|----------|-------|--------|
| 1 | Line 207 | `strings.HasPrefix(filePath, h.dataDir)` fails when dataDir is `/data/reports` and filePath is `/data/reports/report.pdf` due to missing separator | Legitimate files rejected |
| 2 | Line 90 | No URL decoding before path processing | `%2e%2e%2f` can bypass prefix check (attack) |
| 3 | Line 204 | `filepath.Base()` strips subdirectories | Files in subdirectories not downloadable |

## Solution: Approach C - Refactor with filepath.Rel

### Changes to `internal/transport/download.go`

#### 1. Add URL Path Unescape (Line ~90)

Before processing the path, decode URL-encoded characters:

```go
import "net/url"

// In HandleDownload:
rawPath := strings.TrimPrefix(r.URL.Path, "/download/")
path, err := url.PathUnescape(rawPath)
if err != nil {
    http.Error(w, "Invalid path encoding", http.StatusBadRequest)
    return
}
if path == "" || rawPath == r.URL.Path {
    http.Error(w, "Invalid path", http.StatusBadRequest)
    return
}
```

#### 2. Replace Prefix Check with filepath.Rel (Line ~207)

Replace vulnerable `strings.HasPrefix` with idiomatic Go validation:

```go
// Old (vulnerable):
if !strings.HasPrefix(filePath, h.dataDir) {
    http.Error(w, "Access denied", http.StatusForbidden)
    return
}

// New (safe):
rel, err := filepath.Rel(h.dataDir, filePath)
if err != nil || strings.HasPrefix(rel, "..") {
    http.Error(w, "Access denied", http.StatusForbidden)
    return
}
```

#### 3. Remove filepath.Base from Path Construction

The `filepath.Base(filename)` call at line 204 is REMOVED from path construction. Previously it stripped subdirectories, preventing access to files in subdirs. The `filepath.Rel` validation now provides security, making Base redundant for that purpose.

Base is retained only for `Content-Disposition` header (clean filename display), not for security.

### New File: `internal/transport/download_test.go`

Create comprehensive test file covering:

- `TestHandleLocalDownload_PathTraversal` - `../../etc/passwd` → 403
- `TestHandleLocalDownload_EncodedTraversal` - `%2e%2e%2f%2e%2e%2fetc%2fpasswd` → 403
- `TestHandleLocalDownload_Subdirectory` - `subdir/report.pdf` → 200 OK
- `TestHandleLocalDownload_OutsideDataDir` - `reports-secret/file.pdf` → 403
- `TestHandleLocalDownload_FileNotFound` → 404
- `TestHandleLocalDownload_ExpiredFile` → 410
- `TestGenerateLocalURL` - validates URL construction

## Architecture

```
GET /download/local/subdir/report.pdf
  ↓
url.PathUnescape → "subdir/report.pdf"
  ↓
filepath.Join("/data/reports", "subdir/report.pdf") → "/data/reports/subdir/report.pdf"
  ↓
filepath.Rel("/data/reports", "/data/reports/subdir/report.pdf") → "subdir/report.pdf"
  ↓
No starts with ".." → OK
  ↓
os.Stat → exists? → yes
  ↓
time.Since(modTime) < expiry? → yes
  ↓
ServeFile → 200 OK
```

## Security Properties

1. **Path traversal prevention**: `filepath.Rel` correctly handles all edge cases
2. **URL encoding bypass prevention**: `url.PathUnescape` normalizes encoded paths before validation
3. **Subdirectory support**: Files in subdirectories of `dataDir` are now accessible
4. **Defense in depth**: `filepath.Base` still strips dangerous paths as secondary layer

## Testing

Run tests with:
```bash
go test ./internal/transport/... -run TestHandleLocalDownload -v
go test ./internal/transport/... -v  # all transport tests
```

## Implementation Checklist

- [ ] Add `net/url` import to download.go
- [ ] Implement URL path unescape in HandleDownload
- [ ] Replace prefix check with filepath.Rel in handleLocalDownload
- [ ] Create download_test.go with comprehensive test cases
- [ ] Run `go fmt ./... && go vet ./...`
- [ ] Run all tests to verify fix
- [ ] Verify no regression in existing functionality