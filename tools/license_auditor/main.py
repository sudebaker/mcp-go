#!/usr/bin/env python3
import json
import os
import subprocess
import sys
import traceback
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger

logger = get_logger(__name__, "license_auditor")

GO_MODULE_LICENSES = {
    "github.com/gin-gonic/gin": "MIT",
    "github.com/go-kit/kit": "MIT",
    "github.com/golang/protobuf": "BSD-3-Clause",
    "github.com/google/uuid": "BSD-3-Clause",
    "github.com/gorilla/mux": "BSD-3-Clause",
    "github.com/grpc/grpc-go": "Apache-2.0",
    "github.com/hashicorp/consul": "MPL-2.0",
    "github.com/hashicorp/vault": "MPL-2.0",
    "github.com/labstack/echo": "MIT",
    "github.com/mattn/go-sqlite3": "MIT",
    "github.com/prometheus/client_golang": "Apache-2.0",
    "github.com/rs/zerolog": "MIT",
    "github.com/spf13/cobra": "Apache-2.0",
    "github.com/spf13/viper": "MIT",
    "github.com/stretchr/testify": "MIT",
    "github.com/urfave/cli": "MIT",
    "go.uber.org/zap": "MIT",
    "golang.org/x/net": "BSD-3-Clause",
    "golang.org/x/sync": "BSD-3-Clause",
    "golang.org/x/sys": "BSD-3-Clause",
    "golang.org/x/text": "BSD-3-Clause",
    "google.golang.org/grpc": "Apache-2.0",
    "gopkg.in/yaml.v3": "MIT",
    "github.com/mark3labs/mcp-go": "MIT",
    "github.com/sirupsen/logrus": "MIT",
    "github.com/gorilla/websocket": "BSD-2-Clause",
    "github.com/valyala/fasthttp": "MIT",
    "github.com/pelletier/go-toml": "MIT",
    "github.com/fsnotify/fsnotify": "BSD-3-Clause",
    "github.com/magiconair/properties": "BSD-2-Clause",
    "github.com/cenkalti/backoff": "MIT",
    "github.com/docker/docker": "Apache-2.0",
    "github.com/docker/go-connections": "Apache-2.0",
    "github.com/opencontainers/image-spec": "Apache-2.0",
    "golang.org/x/term": "BSD-3-Clause",
    "golang.org/x/crypto": "BSD-3-Clause",
    "golang.org/x/oauth2": "BSD-3-Clause",
    "google.golang.org/genproto": "Apache-2.0",
    "google.golang.org/protobuf": "BSD-3-Clause",
    "github.com/go-sql-driver/mysql": "MPL-2.0",
    "github.com/lib/pq": "MIT",
    "github.com/jackc/pgx": "MIT",
    "github.com/redis/go-redis": "BSD-2-Clause",
    "github.com/minio/minio-go": "Apache-2.0",
    "github.com/aws/aws-sdk-go": "Apache-2.0",
    "github.com/dgraph-io/badger": "Apache-2.0",
    "github.com/nats-io/nats.go": "Apache-2.0",
    "github.com/golang-jwt/jwt": "MIT",
    "github.com/rs/cors": "MIT",
    "github.com/go-chi/chi": "MIT",
    "github.com/gin-contrib/cors": "MIT",
    "github.com/evanphx/json-patch": "BSD-3-Clause",
    "github.com/imdario/mergo": "BSD-3-Clause",
    "github.com/xeipuuv/gojsonschema": "Apache-2.0",
    "github.com/google/go-cmp": "BSD-3-Clause",
    "github.com/google/go-github": "BSD-3-Clause",
    "github.com/pkg/errors": "BSD-2-Clause",
    "github.com/creack/pty": "MIT",
    "github.com/moby/term": "MIT",
    "github.com/Azure/go-ansiterm": "MIT",
    "github.com/morikuni/aec": "MIT",
    "github.com/opencontainers/runc": "Apache-2.0",
    "github.com/containerd/containerd": "Apache-2.0",
    "github.com/Microsoft/go-winio": "MIT",
    "github.com/docker/distribution": "Apache-2.0",
    "github.com/moby/patternmatcher": "Apache-2.0",
    "github.com/moby/sys": "Apache-2.0",
    "github.com/moby/locker": "Apache-2.0",
}

BLACKLIST_DEFAULT = ["GPL", "AGPL", "SSPL", "Proprietary", "BUSL", "BSL"]


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def _pip_installed() -> list[dict[str, str]]:
    result = subprocess.run(
        [sys.executable, "-m", "pip", "list", "--format=json"],
        capture_output=True, text=True, timeout=30,
    )
    if result.returncode != 0:
        logger.warn("pip list failed", error=result.stderr.strip())
        return []
    return json.loads(result.stdout)


def _pip_license(pkg_name: str) -> str | None:
    result = subprocess.run(
        [sys.executable, "-m", "pip", "show", pkg_name],
        capture_output=True, text=True, timeout=10,
    )
    if result.returncode != 0:
        return None
    for line in result.stdout.splitlines():
        if line.lower().startswith("license:"):
            val = line.split(":", 1)[1].strip()
            return val if val and val != "UNKNOWN" else None
    return None


def scan_python() -> list[dict[str, Any]]:
    deps = _pip_installed()
    results = []
    for dep in deps:
        name = dep.get("name", "")
        version = dep.get("version", "")
        license_ = _pip_license(name)
        results.append({
            "package": name,
            "version": version,
            "license": license_ or "Unknown",
            "ecosystem": "python",
        })
    return results


def scan_go(project_root: str) -> list[dict[str, Any]]:
    go_mod = Path(project_root) / "go.mod"
    if not go_mod.exists():
        return []
    deps = []
    with open(go_mod) as f:
        in_require = False
        for line in f:
            if line.startswith("require ("):
                in_require = True
                continue
            if in_require and line.startswith(")"):
                in_require = False
                continue
            if in_require:
                parts = line.strip().split()
                if len(parts) >= 2:
                    mod_path = parts[0]
                    version = parts[1]
                    license_ = None
                    for prefix, lic in GO_MODULE_LICENSES.items():
                        if mod_path == prefix or mod_path.startswith(prefix + "/"):
                            license_ = lic
                            break
                    deps.append({
                        "package": mod_path,
                        "version": version,
                        "license": license_ or "Unknown",
                        "ecosystem": "go",
                    })
            elif line.startswith("require "):
                parts = line.strip().split()
                if len(parts) >= 3:
                    mod_path = parts[1]
                    version = parts[2]
                    license_ = None
                    for prefix, lic in GO_MODULE_LICENSES.items():
                        if mod_path == prefix or mod_path.startswith(prefix + "/"):
                            license_ = lic
                            break
                    deps.append({
                        "package": mod_path,
                        "version": version,
                        "license": license_ or "Unknown",
                        "ecosystem": "go",
                    })
    return deps


def scan_npm(project_root: str) -> list[dict[str, Any]]:
    pkg_json = Path(project_root) / "package.json"
    if not pkg_json.exists():
        return []
    with open(pkg_json) as f:
        data = json.load(f)

    deps = []
    for section in ("dependencies", "devDependencies", "peerDependencies"):
        for pkg_name, version in data.get(section, {}).items():
            license_ = None
            node_pkg = Path(project_root) / "node_modules" / pkg_name / "package.json"
            if node_pkg.exists():
                with open(node_pkg) as f2:
                    meta = json.load(f2)
                lic = meta.get("license")
                if lic and lic != "UNKNOWN":
                    license_ = lic
                elif "licenses" in meta:
                    lic_arr = meta["licenses"]
                    if isinstance(lic_arr, list) and len(lic_arr) > 0:
                        license_ = lic_arr[0].get("type", lic_arr[0].get("type"))
            deps.append({
                "package": pkg_name,
                "version": version,
                "license": license_ or "Unknown",
                "ecosystem": "npm",
            })
    return deps


def resolve_ecosystems(ecosystems: list[str], project_root: str) -> list[str]:
    if "auto" not in ecosystems:
        return ecosystems
    detected = []
    if Path(project_root, "go.mod").exists():
        detected.append("go")
    if Path(project_root, "package.json").exists():
        detected.append("npm")
    detected.append("python")
    return detected


def format_blocked_table(blocked: list[dict[str, Any]]) -> str:
    lines = [
        "| Package | Version | License | Ecosystem |",
        "|---------|---------|---------|-----------|",
    ]
    for dep in blocked:
        lines.append(
            f"| {dep['package']} | {dep['version']} | {dep['license']} | {dep['ecosystem']} |"
        )
    return "\n".join(lines)


def format_summary_table(all_deps: list[dict[str, Any]]) -> str:
    lines = [
        "| Package | Version | License | Ecosystem |",
        "|---------|---------|---------|-----------|",
    ]
    for dep in all_deps:
        lines.append(
            f"| {dep['package']} | {dep['version']} | {dep['license']} | {dep['ecosystem']} |"
        )
    return "\n".join(lines)


def main():
    try:
        request = read_request()
        args = request.get("arguments", {})
        project_root = args.get("project_root", "/app")
        ecosystems = args.get("ecosystems", ["auto"])
        blocklist = args.get("blocklist", BLACKLIST_DEFAULT)

        if not Path(project_root).exists():
            write_response({
                "success": False,
                "error": {"code": "INVALID_INPUT", "message": f"Path not found: {project_root}"},
                "request_id": request.get("request_id", ""),
            })
            return

        resolved_ecosystems = resolve_ecosystems(ecosystems, project_root)

        all_deps: list[dict[str, Any]] = []

        if "python" in resolved_ecosystems:
            logger.info("Scanning Python dependencies...")
            all_deps.extend(scan_python())

        if "go" in resolved_ecosystems:
            logger.info("Scanning Go dependencies...")
            all_deps.extend(scan_go(project_root))

        if "npm" in resolved_ecosystems:
            logger.info("Scanning npm dependencies...")
            all_deps.extend(scan_npm(project_root))

        blocked = [dep for dep in all_deps if any(
            bl.lower() in dep["license"].lower() for bl in blocklist
        )]

        if blocked:
            blocked_text = format_blocked_table(blocked)
            summary = (
                f"## License Audit Results\n\n"
                f"### Blocked Dependencies ({len(blocked)})\n\n"
                f"{blocked_text}\n\n"
                f"### All Dependencies ({len(all_deps)})\n\n"
                f"{format_summary_table(all_deps)}"
            )
            write_response({
                "success": True,
                "content": [{"type": "text", "text": summary}],
                "request_id": request.get("request_id", ""),
                "structured_content": {
                    "total_deps": len(all_deps),
                    "blocked_count": len(blocked),
                    "blocked": blocked,
                    "ecosystems_found": resolved_ecosystems,
                },
            })
        else:
            summary = (
                f"## License Audit Results\n\n"
                f"No blocked dependencies found. All {len(all_deps)} dependencies have acceptable licenses.\n\n"
                f"### All Dependencies ({len(all_deps)})\n\n"
                f"{format_summary_table(all_deps)}"
            )
            write_response({
                "success": True,
                "content": [{"type": "text", "text": summary}],
                "request_id": request.get("request_id", ""),
                "structured_content": {
                    "total_deps": len(all_deps),
                    "blocked_count": 0,
                    "blocked": [],
                    "ecosystems_found": resolved_ecosystems,
                },
            })
    except subprocess.TimeoutExpired:
        write_response({
            "success": False,
            "error": {"code": "TIMEOUT", "message": "License scanning timed out"},
            "request_id": request.get("request_id", ""),
        })
    except Exception as e:
        write_response({
            "success": False,
            "error": {"code": "EXECUTION_FAILED", "message": str(e), "details": traceback.format_exc()},
            "request_id": request.get("request_id", ""),
        })


if __name__ == "__main__":
    main()
