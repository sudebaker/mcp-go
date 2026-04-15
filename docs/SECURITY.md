# Security Hardening: RustFS/S3 Integration

## Overview

This document describes the security measures implemented for the RustFS/S3 integration in the MCP Orchestrator, including SSRF (Server-Side Request Forgery) protection, DoS prevention, and configuration requirements.

## Security Vulnerabilities Fixed

### Fix #1: SSRF Protection via SSRF_ALLOWLIST

**Vulnerability:** Substring hostname matching in `is_rustfs_url()` allowed SSRF bypass attacks.

**Example Attack:** 
- Configured endpoint: `rustfs:9000`
- Old vulnerable check: `rustfs_host in hostname` 
- Attack URL: `http://evilrustfs.com/openwebui/file.csv` would pass (substring "rustfs" found)

**Solution:** 
- Use `is_internal_url()` from `tools/common/validators.py` for SSRF validation
- Requires exact hostname match against configured `RUSTFS_ENDPOINT`
- Respects `SSRF_ALLOWLIST` environment variable for whitelisting allowed internal hosts

**Implementation Details:**
```python
def is_rustfs_url(url: str) -> bool:
    """
    1. Check if URL is blocked by is_internal_url() (returns False if blocked)
    2. Extract hostname from RUSTFS_ENDPOINT
    3. Perform exact hostname match (no substring matching)
    """
    if is_internal_url(url):
        return False  # Blocked by SSRF protection
    
    # ... exact hostname match logic
```

**Required Configuration:**
```yaml
# deployments/docker-compose.yml
environment:
  SSRF_ALLOWLIST: "rustfs"  # Whitelist rustfs as allowed internal host
  RUSTFS_ENDPOINT: "rustfs:9000"  # Must be exact match
```

---

### Fix #2: DoS Prevention via Base64 Size Limits

**Vulnerability:** Unlimited base64 file uploads could cause Out-Of-Memory (OOM) or DoS.

**Example Attack:**
- Send 1GB base64-encoded content in `__files__` parameter
- No validation on decoded content size
- Application crashes or becomes unresponsive

**Solution:**
- Validate decoded base64 content size against `MAX_FILE_SIZE_MB` (100MB default)
- Reject uploads exceeding limit with clear error message

**Implementation Details:**
```python
def load_data_from_base64(content: str, filename: str) -> "pd.DataFrame":
    # ... decode base64 ...
    size_mb = len(content_bytes) / (1024 * 1024)
    if size_mb > MAX_FILE_SIZE_MB:
        raise ValueError(
            f"Decoded file size ({size_mb:.2f}MB) exceeds maximum allowed size ({MAX_FILE_SIZE_MB}MB)"
        )
```

**Configuration:**
```python
# tools/data_analysis/main.py
MAX_FILE_SIZE_MB = 100  # Limit per file
```

---

### Fix #3: S3 Operation Timeouts

**Vulnerability:** S3 operations could block indefinitely without timeout protection.

**Example Attack:**
- S3 server slow to respond or unresponsive
- `client.get_object()` and `response.read()` block forever
- Worker threads exhaust, application hangs

**Solution:**
- Wrap S3 operations with signal-based timeout (default: 30 seconds)
- Configurable via `S3_OPERATION_TIMEOUT_SECONDS` environment variable
- Raises `TimeoutError` if operation exceeds timeout

**Implementation Details:**
```python
def download_from_s3(url: str, client: Minio) -> BytesIO:
    timeout_seconds = int(os.environ.get("S3_OPERATION_TIMEOUT_SECONDS", "30"))
    
    # Set alarm signal for timeout protection
    signal.signal(signal.SIGALRM, timeout_handler)
    signal.alarm(timeout_seconds)
    
    try:
        response = client.get_object(bucket, key)
        data = response.read()  # Protected by alarm
        signal.alarm(0)  # Cancel alarm
    except TimeoutError:
        raise Exception(f"S3 download timed out after {timeout_seconds}s")
```

**Configuration:**
```yaml
environment:
  S3_OPERATION_TIMEOUT_SECONDS: "30"  # Default: 30 seconds
```

---

### Fix #4: Presigned URL TTL Configuration

**Vulnerability:** Hardcoded 1-hour TTL for presigned URLs not configurable.

**Issue:** 
- Different deployments need different TTL values
- Security/compliance requirements vary (short-lived URLs vs. long-lived)
- No way to adjust without code changes

**Solution:**
- Make presigned URL TTL configurable via `RUSTFS_PRESIGNED_TTL_SECONDS`
- Default: 3600 seconds (1 hour)
- Applied to both upload and download operations

**Implementation Details:**
```python
# tools/rustfs_storage/main.py
PRESIGNED_URL_TTL_SECONDS = int(os.environ.get("RUSTFS_PRESIGNED_TTL_SECONDS", "3600"))

# In operation_upload and operation_download:
presigned_url = client.presigned_get_object(
    bucket, key, expires=timedelta(seconds=PRESIGNED_URL_TTL_SECONDS)
)
```

**Configuration:**
```yaml
environment:
  RUSTFS_PRESIGNED_TTL_SECONDS: "3600"  # Default: 1 hour
```

---

## SSRF_ALLOWLIST Configuration Guide

The `SSRF_ALLOWLIST` environment variable controls which internal hosts/IPs are allowed to be accessed via URL operations.

### Format
Comma-separated list of:
- **Hostnames**: `rustfs`, `myservice.corp`, `internal-db.local`
- **CIDR ranges**: `192.168.1.0/24`, `10.0.0.0/8`, `172.16.0.0/12`

### Examples

**Single hostname (default for RustFS):**
```yaml
SSRF_ALLOWLIST: "rustfs"
```

**Multiple services:**
```yaml
SSRF_ALLOWLIST: "rustfs,database.internal,cache.local"
```

**Mix of hostnames and CIDR ranges:**
```yaml
SSRF_ALLOWLIST: "rustfs,192.168.1.0/24,10.0.0.0/8,myservice.corp"
```

### Validation Logic

When a tool tries to access a URL:

1. **Parse hostname** from URL
2. **Check against BLOCKED_HOSTS** (cloud metadata endpoints - always blocked):
   - `169.254.169.254` (AWS/Azure)
   - `metadata.google.internal` (GCP)
   - `instance-data` (OpenStack)
3. **Check for private IP ranges** (RFC-1918, link-local, loopback):
   - `10.0.0.0/8`
   - `172.16.0.0/12`
   - `192.168.0.0/16`
   - `127.0.0.0/8` (loopback)
   - `169.254.0.0/16` (link-local)
   - `fc00::/7` (IPv6 private)
   - `fe80::/10` (IPv6 link-local)
4. **Check for .local/.localhost/.internal domains**
5. **If any match, check SSRF_ALLOWLIST**:
   - If hostname/IP in allowlist → **ALLOW**
   - Otherwise → **DENY** (return False from `is_internal_url()`)
6. **If external URL** → **ALLOW** (unless it's a known metadata endpoint)

### Security Properties

- **Cloud metadata is NEVER allowlisted** even if in `SSRF_ALLOWLIST` (hardcoded blocklist)
- **Private IPs require explicit allowlist entry** to be accessible
- **External URLs are allowed by default** unless they match cloud metadata patterns
- **Case-insensitive matching** for hostnames

---

## Environment Variable Reference

| Variable | Default | Description | Security Impact |
|----------|---------|-------------|-----------------|
| `SSRF_ALLOWLIST` | `rustfs` | Comma-separated list of allowed internal hosts/CIDR ranges | **Critical** - Controls SSRF vulnerability |
| `RUSTFS_ENDPOINT` | `rustfs:9000` | RustFS service endpoint | **Critical** - Must match exact hostname |
| `S3_OPERATION_TIMEOUT_SECONDS` | `30` | Timeout for S3 operations (get_object, read) | **High** - Prevents indefinite blocking |
| `RUSTFS_PRESIGNED_TTL_SECONDS` | `3600` | Time-to-live for presigned URLs in seconds | **Medium** - Controls URL validity window |

---

## Testing Security Fixes

### Unit Tests
Run security tests with:
```bash
python -m pytest tests/test_rustfs_integration.py::TestSecurityFix1_SSRFProtection -v
python -m pytest tests/test_rustfs_integration.py::TestSecurityFix2_Base64SizeLimit -v
python -m pytest tests/test_rustfs_integration.py::TestSecurityFix3_S3Timeouts -v
python -m pytest tests/test_rustfs_integration.py::TestSecurityFix4_PresignedURLTTL -v
python -m pytest tests/test_rustfs_integration.py::TestSecurityFix5_Documentation -v
```

### Integration Tests
```bash
python -m pytest tests/test_rustfs_integration.py -v
```

### Manual Testing

**Test SSRF protection:**
```bash
# Should succeed (exact match)
curl http://localhost:8000/api/analyze_data \
  -H "Content-Type: application/json" \
  -d '{"files": [{"url": "http://rustfs:9000/openwebui/test.csv"}]}'

# Should fail (SSRF bypass attempt)
curl http://localhost:8000/api/analyze_data \
  -H "Content-Type: application/json" \
  -d '{"files": [{"url": "http://evilrustfs.com/test.csv"}]}'
```

**Test base64 size limit:**
```bash
# Should fail (>100MB)
python -c "import base64; print(base64.b64encode(b'x' * (101 * 1024 * 1024)).decode())"
```

**Test S3 timeout:**
```bash
# Set a very short timeout to test timeout handling
export S3_OPERATION_TIMEOUT_SECONDS=1
# Access slow S3 server will now timeout
```

---

## Deployment Checklist

- [ ] Set `SSRF_ALLOWLIST` environment variable (at minimum: `rustfs`)
- [ ] Verify `RUSTFS_ENDPOINT` matches the actual RustFS hostname
- [ ] Configure `S3_OPERATION_TIMEOUT_SECONDS` appropriate for your network latency
- [ ] Set `RUSTFS_PRESIGNED_TTL_SECONDS` based on security requirements
- [ ] Run security tests before deploying: `pytest tests/test_rustfs_integration.py`
- [ ] Review SSRF_ALLOWLIST entries - ensure no overly broad CIDR ranges
- [ ] Monitor logs for SSRF or timeout errors during initial deployment
- [ ] Document your SSRF_ALLOWLIST configuration for your ops team

---

## Related Security Measures

### Other Protections in Place

1. **Path Traversal Prevention** (`tools/common/validators.py`)
   - `validate_file_path()` - Prevents `../` attacks
   - `validate_read_path()` / `validate_write_path()` - Sandboxed file access

2. **Input Validation**
   - File size limits per tool
   - Question/prompt length limits
   - Filename sanitization

3. **Sandboxed Code Execution** (`tools/common/sandbox.py`)
   - Memory limits on executed code
   - Timeout protection
   - Resource limits

4. **Logging & Monitoring**
   - All SSRF blocks logged
   - Timeout events logged
   - File access attempts logged

---

## Reporting Security Issues

If you discover a security vulnerability, please report it privately to the maintainers rather than disclosing it publicly.

Contact: [security contact info]

Please include:
- Description of vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

---

## References

- [OWASP: Server-Side Request Forgery (SSRF)](https://owasp.org/www-community/attacks/Server-Side_Request_Forgery)
- [OWASP: Denial of Service (DoS)](https://owasp.org/www-community/attacks/Denial_of_Service)
- [CWE-918: Server-Side Request Forgery (SSRF)](https://cwe.mitre.org/data/definitions/918.html)
- [RFC 1918: Private Internet Addresses](https://tools.ietf.org/html/rfc1918)

---

**Document Version:** 1.0  
**Last Updated:** 2025-04-15  
**Status:** Active
