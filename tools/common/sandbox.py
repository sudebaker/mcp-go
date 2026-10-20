#!/usr/bin/env python3
"""
Docker-based sandbox executor for secure code execution.

Provides isolated execution environments with resource limits.
Container is always removed after execution (auto_remove=True).
"""

import json
import os
import time
import urllib.error
import urllib.request
import uuid
from typing import Any, Callable, Dict, List, Optional, Union

try:
    import docker
    from docker.errors import APIError, BuildError, DockerException, ImageNotFound

    DOCKER_AVAILABLE = True
except ImportError:
    DOCKER_AVAILABLE = False

    class _DockerUnavailable(Exception):
        """Stand-in for the docker.errors types when the SDK is not installed."""

    APIError = BuildError = DockerException = ImageNotFound = _DockerUnavailable


CHUNK_PREFIX = "__CHUNK__:"
RESULT_PREFIX = "__RESULT__:"

# Canonical tag of the sandbox image. It is the only name the tool builds and
# runs; SANDBOX_IMAGE overrides it for deployments that pre-build under another
# tag (the image itself is identical).
DEFAULT_SANDBOX_IMAGE = "mcp-python-sandbox:latest"

# The Dockerfile lives in tools/data_analysis/ but its build context is the
# repository root: it copies tools/common/*.py and
# tools/data_analysis/sandbox_bootstrap.py. This is the same context CI uses.
SANDBOX_DOCKERFILE = os.path.join("tools", "data_analysis", "sandbox.Dockerfile")


def default_sandbox_image() -> str:
    """Canonical image tag, or the SANDBOX_IMAGE override when set."""
    return os.environ.get("SANDBOX_IMAGE") or DEFAULT_SANDBOX_IMAGE


def sandbox_build_context() -> str:
    """Repository root, i.e. the Docker build context sandbox.Dockerfile expects."""
    # <root>/tools/common/sandbox.py -> <root>
    return os.path.dirname(
        os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    )


class SandboxConfig:
    def __init__(
        self,
        image: Optional[str] = None,
        timeout_seconds: int = 60,
        memory_limit: str = "512m",
        cpu_limit: float = 0.5,
        pids_limit: int = 50,
        network_disabled: bool = True,
        build_on_missing: bool = True,
        readonly_dir: str = "/data/input",
        writable_dir: str = "/data/output",
        max_file_size_mb: int = 100,
    ):
        self.image = image or default_sandbox_image()
        self.timeout = timeout_seconds
        self.memory = memory_limit
        self.cpu = cpu_limit
        self.pids = pids_limit
        self.network_disabled = network_disabled
        self.build_on_missing = build_on_missing
        self.readonly_dir = readonly_dir
        self.writable_dir = writable_dir
        self.max_file_size_mb = max_file_size_mb


class SandboxResult:
    def __init__(
        self,
        success: bool,
        output: str = "",
        error: Optional[str] = None,
        chunks: Optional[list] = None,
        files: Optional[Dict[str, str]] = None,
        execution_time_ms: int = 0,
        exit_code: int = 0,
    ):
        self.success = success
        self.output = output
        self.error = error
        self.chunks = chunks or []
        self.files = files or {}
        self.execution_time_ms = execution_time_ms
        self.exit_code = exit_code

    def to_dict(self) -> dict:
        return {
            "success": self.success,
            "output": self.output,
            "error": self.error,
            "chunks": self.chunks,
            "files": self.files,
            "execution_time_ms": self.execution_time_ms,
            "exit_code": self.exit_code,
        }


class DockerSandboxedExecutor:
    CHUNK_CALLBACK = None

    def __init__(self, config: Optional[SandboxConfig] = None):
        self.config = config or SandboxConfig()
        self._client = None

    @property
    def client(self):
        if self._client is None:
            if not DOCKER_AVAILABLE:
                raise RuntimeError("docker package not installed")
            self._client = docker.from_env()
        return self._client

    def set_chunk_callback(self, callback: Callable[[dict], None]):
        DockerSandboxedExecutor.CHUNK_CALLBACK = callback

    def emit_chunk(self, chunk_type: str, data: Any):
        chunk = {"type": chunk_type, "data": data}
        if DockerSandboxedExecutor.CHUNK_CALLBACK:
            DockerSandboxedExecutor.CHUNK_CALLBACK(chunk)
        return chunk

    def build_image(
        self,
        dockerfile_path: Optional[str] = None,
        tag: Optional[str] = None,
        context: Optional[str] = None,
    ) -> bool:
        """Build the sandbox image from its Dockerfile.

        The context defaults to the repository root because that is what
        sandbox.Dockerfile expects (``COPY tools/common/*.py``). Passing a
        Dockerfile outside that context is a programming error: the build would
        fail on the COPY, so it is rejected before talking to the daemon.
        """
        tag = tag or self.config.image
        build_context = os.path.abspath(context or sandbox_build_context())

        dockerfile = dockerfile_path or SANDBOX_DOCKERFILE
        if not os.path.isabs(dockerfile):
            dockerfile = os.path.join(build_context, dockerfile)
        dockerfile = os.path.abspath(dockerfile)

        relative_dockerfile = os.path.relpath(dockerfile, build_context)
        if relative_dockerfile == os.pardir or relative_dockerfile.startswith(
            os.pardir + os.sep
        ):
            self.emit_chunk(
                "error",
                {
                    "message": f"Dockerfile {dockerfile} is outside the build "
                    f"context {build_context}"
                },
            )
            return False
        if not os.path.isfile(dockerfile):
            self.emit_chunk(
                "error", {"message": f"Dockerfile not found: {dockerfile}"}
            )
            return False

        try:
            self.client.images.build(
                path=build_context,
                dockerfile=relative_dockerfile,
                tag=tag,
                rm=True,
            )
            return True
        except (BuildError, DockerException) as e:
            self.emit_chunk("error", {"message": f"Failed to build image: {e}"})
            return False

    def _ensure_image_exists(self) -> bool:
        try:
            self.client.images.get(self.config.image)
            return True
        except ImageNotFound:
            pass
        except APIError as e:
            self.emit_chunk(
                "error",
                {
                    "message": "Docker is not reachable, cannot use "
                    f"{self.config.image}: {e}"
                },
            )
            return False

        if not self.config.build_on_missing:
            return False

        dockerfile = os.path.join(sandbox_build_context(), SANDBOX_DOCKERFILE)
        if not os.path.isfile(dockerfile):
            self.emit_chunk(
                "error",
                {
                    "message": f"Image {self.config.image} is missing and "
                    f"{dockerfile} was not found"
                },
            )
            return False
        self.emit_chunk("status", {"message": f"Building image {self.config.image}"})
        return self.build_image(dockerfile)

    def execute(
        self,
        code: str,
        input_data: Optional[Union[Dict[str, Any], List[Any]]] = None,
        env_vars: Optional[Dict[str, str]] = None,
    ) -> SandboxResult:
        start_time = time.time()

        if not DOCKER_AVAILABLE:
            return SandboxResult(
                success=False,
                error="Docker Python SDK not available",
                execution_time_ms=int((time.time() - start_time) * 1000),
            )

        if not self._ensure_image_exists():
            return SandboxResult(
                success=False,
                error=f"Image {self.config.image} not available",
                execution_time_ms=int((time.time() - start_time) * 1000),
            )

        try:
            input_csv = ""
            if input_data is not None:
                import pandas as pd

                df = (
                    pd.DataFrame(input_data)
                    if isinstance(input_data, list)
                    else pd.DataFrame([input_data])
                )
                input_csv = df.to_csv(index=False)

            wrapped_code = self._wrap_code(code, input_csv)

            volumes = {
                "/tmp/input": {"bind": "/tmp/input", "mode": "ro"},
                "/tmp/output": {"bind": "/tmp/output", "mode": "rw"},
            }

            self.emit_chunk("status", {"message": "Starting container"})

            result = self.client.containers.run(
                image=self.config.image,
                command=["python3", "-c", wrapped_code],
                remove=True,
                network="none" if self.config.network_disabled else None,
                mem_limit=self.config.memory,
                memswap_limit=self.config.memory,
                cpu_period=100000,
                cpu_quota=int(self.config.cpu * 100000),
                pids_limit=self.config.pids,
                read_only=True,
                tmpfs={"/tmp": "size=64M,mode=1777"},
                user="nobody",
                working_dir="/tmp",
                volumes=volumes,
            )

            self.emit_chunk("status", {"message": "Container completed"})

            logs = result.decode()
            chunks, _ = self._parse_output(logs)

            for chunk in chunks:
                self.emit_chunk(chunk.get("type", "unknown"), chunk.get("data", {}))

            execution_time = int((time.time() - start_time) * 1000)

            return SandboxResult(
                success=True,
                output=logs,
                chunks=chunks,
                execution_time_ms=execution_time,
            )

        except Exception as e:
            execution_time = int((time.time() - start_time) * 1000)
            self.emit_chunk("error", {"message": str(e)})
            return SandboxResult(
                success=False,
                error=str(e),
                execution_time_ms=execution_time,
            )

    def execute_with_volume(
        self,
        code: str,
        host_path: str,
        input_data: Optional[Dict[str, Any]] = None,
        env_vars: Optional[Dict[str, str]] = None,
    ) -> SandboxResult:
        start_time = time.time()

        if not DOCKER_AVAILABLE:
            return SandboxResult(
                success=False,
                error="Docker Python SDK not available",
                execution_time_ms=int((time.time() - start_time) * 1000),
            )

        if not self._ensure_image_exists():
            return SandboxResult(
                success=False,
                error=f"Image {self.config.image} not available",
                execution_time_ms=int((time.time() - start_time) * 1000),
            )

        try:
            import base64
            import tempfile
            import os

            encoded_code = base64.b64encode(code.encode()).decode()

            output_dir = tempfile.mkdtemp(prefix="sandbox_output_")
            os.chmod(output_dir, 0o777)

            volumes = {
                host_path: {"bind": "/data", "mode": "ro"},
                output_dir: {"bind": "/tmp/output", "mode": "rw"},
            }

            env = env_vars.copy() if env_vars else {}
            env["MCP_SANDBOX_CODE"] = encoded_code
            env["MPLCONFIGDIR"] = "/tmp/matplotlib"

            self.emit_chunk("status", {"message": "Starting container"})

            result = self.client.containers.run(
                image=self.config.image,
                command=["python3", "/sandbox/sandbox_bootstrap.py"],
                remove=True,
                network="none" if self.config.network_disabled else None,
                mem_limit=self.config.memory,
                memswap_limit=self.config.memory,
                cpu_period=100000,
                cpu_quota=int(self.config.cpu * 100000),
                pids_limit=self.config.pids,
                read_only=True,
                tmpfs={"/tmp": "size=64M,mode=1777"},
                user="nobody",
                working_dir="/data",
                volumes=volumes,
                environment=env,
            )

            self.emit_chunk("status", {"message": "Container completed"})

            logs = result.decode()
            chunks, _ = self._parse_output(logs)

            for chunk in chunks:
                self.emit_chunk(chunk.get("type", "unknown"), chunk.get("data", {}))

            files = {}
            if os.path.exists(output_dir):
                for filename in os.listdir(output_dir):
                    filepath = os.path.join(output_dir, filename)
                    if os.path.isfile(filepath):
                        with open(filepath, "rb") as f:
                            files[filename] = base64.b64encode(f.read()).decode()
                try:
                    os.rmdir(output_dir)
                except Exception:
                    pass

            execution_time = int((time.time() - start_time) * 1000)

            return SandboxResult(
                success=True,
                output=logs,
                chunks=chunks,
                files=files,
                execution_time_ms=execution_time,
            )

        except Exception as e:
            execution_time = int((time.time() - start_time) * 1000)
            self.emit_chunk("error", {"message": str(e)})
            return SandboxResult(
                success=False,
                error=str(e),
                execution_time_ms=execution_time,
            )

    def _wrap_code(self, code: str, input_csv: str) -> str:
        import base64

        encoded_code = base64.b64encode(code.encode()).decode()
        encoded_input = (
            base64.b64encode(input_csv.encode()).decode() if input_csv else ""
        )

        wrapper = """
import base64
import sys
import json as _json
import pandas as pd
from io import StringIO

CHUNK_PREFIX = "__CHUNK__:"
RESULT_PREFIX = "__RESULT__:"

def emit_chunk(chunk_type, data):
    payload = _json.dumps({"type": chunk_type, "data": data})
    sys.stdout.write(CHUNK_PREFIX + payload + "\\n")
    sys.stdout.flush()

def emit_result(success, output=None, structured=None, files=None):
    result = {
        "success": success,
        "output": output or "",
        "structured": structured or {},
        "files": files or {}
    }
    payload = _json.dumps(result)
    sys.stdout.write(RESULT_PREFIX + payload + "\\n")
    sys.stdout.flush()

code = base64.b64decode("CODE_PLACEHOLDER").decode()

if "INPUT_PLACEHOLDER":
    try:
        csv_data = base64.b64decode("INPUT_PLACEHOLDER").decode()
        df = pd.read_csv(StringIO(csv_data))
    except Exception as e:
        emit_chunk("error", {"message": str(e)})
        emit_result(False, str(e))
        sys.exit(1)

restricted_globals = {
    "_builtins_": {
        "print": print,
        "len": len,
        "str": str,
        "int": int,
        "float": float,
        "list": list,
        "dict": dict,
        "tuple": tuple,
        "set": set,
        "range": range,
        "abs": abs,
        "max": max,
        "min": min,
        "sum": sum,
        "sorted": sorted,
        "zip": zip,
        "map": map,
        "filter": filter,
        "round": round,
        "enumerate": enumerate,
        "reversed": reversed,
        "isinstance": isinstance,
        "hasattr": hasattr,
        "getattr": getattr,
        "setattr": setattr,
        "delattr": delattr,
        "open": None,
        "__import__": None,
        "compile": None,
        "exec": None,
        "eval": None,
        "execfile": None,
    },
    "pd": pd,
    "plt": None,
    "np": None,
    "df": df,
    "emit_chunk": emit_chunk,
    "emit_result": emit_result,
}

def main():
    emit_chunk("status", {"message": "Executing code"})
    try:
        exec(code, restricted_globals)
        emit_chunk("status", {"message": "Execution completed"})
        emit_result(True, "Code executed successfully")
    except Exception as e:
        import traceback
        emit_chunk("error", {"message": str(e), "traceback": traceback.format_exc()})
        emit_result(False, str(e))
        sys.exit(1)

if __name__ == "__main__":
    main()
"""
        wrapper = wrapper.replace("CODE_PLACEHOLDER", encoded_code)
        wrapper = wrapper.replace("INPUT_PLACEHOLDER", encoded_input)

        return wrapper

    def _parse_output(self, logs: str) -> tuple:
        chunks = []
        for line in logs.split("\n"):
            line = line.strip()
            if line.startswith(CHUNK_PREFIX):
                try:
                    data = json.loads(line[len(CHUNK_PREFIX) :])
                    chunks.append(data)
                except json.JSONDecodeError:
                    pass
        return chunks, {}

    def _validate_code_safety(self, code: str) -> tuple[bool, Optional[str]]:
        """
        Validate code for dangerous operations using AST parsing.
        Returns (is_safe, error_message).
        """
        import ast

        ALLOWED_IMPORTS = {
            # Data processing core
            "pandas",
            "pd",
            "numpy",
            "np",
            # Excel and file formats
            "openpyxl",
            "xlrd",
            "xlsxwriter",
            "xlwt",
            "csv",
            "json",
            "yaml",
            # Visualization
            "matplotlib",
            "plt",
            "seaborn",
            "sns",
            "plotly",
            # File and path handling
            "pathlib",
            "os.path",
            "io",
            "tempfile",
            "shutil",
            # Data utilities
            "datetime",
            "time",
            "math",
            "statistics",
            "collections",
            "itertools",
            "functools",
            "re",
            "base64",
            "hashlib",
            # Additional data science
            "scipy",
            "sklearn",
            "scikit-learn",
        }

        DANGEROUS_NAMES = {
            "__import__",
            "eval",
            "exec",
            "compile",
            "file",
            "input",
            "raw_input",
            "execfile",
            "__builtins__",
            "globals",
            "locals",
            "vars",
            "dir",
            "help",
            "reload",
            "__dict__",
            "__class__",
            "__bases__",
            "__subclasses__",
            "__mro__",
        }

        DANGEROUS_ATTRS = {
            "__code__",
            "__globals__",
            "__closure__",
            "__dict__",
            "__class__",
            "__bases__",
            "__subclasses__",
            "__import__",
        }

        try:
            tree = ast.parse(code)
        except SyntaxError as e:
            return False, f"Syntax error: {e}"

        for node in ast.walk(tree):
            if isinstance(node, ast.Import):
                for alias in node.names:
                    module_parts = alias.name.split(".")
                    module_base = module_parts[0]

                    if (
                        module_base not in ALLOWED_IMPORTS
                        and alias.name not in ALLOWED_IMPORTS
                    ):
                        return (
                            False,
                            f"Import of '{alias.name}' not allowed. Allowed: {', '.join(sorted(ALLOWED_IMPORTS))}",
                        )

            if isinstance(node, ast.ImportFrom):
                module_parts = node.module.split(".") if node.module else []
                module_base = module_parts[0] if module_parts else ""

                if (
                    module_base not in ALLOWED_IMPORTS
                    and node.module not in ALLOWED_IMPORTS
                ):
                    return (
                        False,
                        f"Import from '{node.module}' not allowed. Allowed: {', '.join(sorted(ALLOWED_IMPORTS))}",
                    )

            if isinstance(node, ast.Name) and node.id in DANGEROUS_NAMES:
                return False, f"Dangerous name '{node.id}' not allowed"

            if isinstance(node, ast.Attribute):
                if node.attr in DANGEROUS_ATTRS:
                    return False, f"Dangerous attribute '.{node.attr}' not allowed"

                if (
                    node.attr.startswith("_")
                    and not node.attr.startswith("__")
                    and not node.attr.endswith("__")
                ):
                    return False, f"Private attribute access not allowed: {node.attr}"

            if isinstance(node, ast.Call):
                if isinstance(node.func, ast.Name):
                    func_name = node.func.id

                    if func_name in DANGEROUS_NAMES:
                        return False, f"Dangerous function '{func_name}' not allowed"

                    if func_name == "open":
                        return (
                            False,
                            "Direct 'open()' not allowed. Use 'open_read()' or 'open_write()' instead",
                        )

        return True, None

    def execute_fallback(
        self,
        code: str,
        input_data: Optional[Union[Dict[str, Any], List[Any]]] = None,
        on_chunk: Optional[Callable[[dict], None]] = None,
    ) -> SandboxResult:
        if on_chunk:
            self.set_chunk_callback(on_chunk)

        is_safe, error_msg = self._validate_code_safety(code)
        if not is_safe:
            return SandboxResult(
                success=False,
                error=f"Code validation failed: {error_msg}",
                execution_time_ms=0,
            )

        self.emit_chunk(
            "warning",
            {"message": "Using restricted exec() fallback - limited functionality"},
        )

        start_time = time.time()

        try:
            import pandas as pd
            import numpy as np

            df = None
            if input_data is not None:
                df = (
                    pd.DataFrame(input_data)
                    if isinstance(input_data, list)
                    else pd.DataFrame([input_data])
                )

            try:
                from safe_file_ops import SafeFileOperations

                file_ops = SafeFileOperations(
                    readonly_dir=getattr(self.config, "readonly_dir", "/data/input"),
                    writable_dir=getattr(self.config, "writable_dir", "/data/output"),
                    max_file_size_mb=getattr(self.config, "max_file_size_mb", 100),
                )
            except ImportError:
                file_ops = None
                import logging

                logging.warning(
                    "SafeFileOperations not available, file operations disabled"
                )

            safe_builtins = {
                "print": print,
                "len": len,
                "str": str,
                "int": int,
                "float": float,
                "bool": bool,
                "list": list,
                "tuple": tuple,
                "dict": dict,
                "set": set,
                "range": range,
                "abs": abs,
                "max": max,
                "min": min,
                "sum": sum,
                "sorted": sorted,
                "reversed": reversed,
                "enumerate": enumerate,
                "zip": zip,
                "map": map,
                "filter": filter,
                "any": any,
                "all": all,
                "round": round,
                "pow": pow,
                "divmod": divmod,
                "isinstance": isinstance,
                "issubclass": issubclass,
                "type": type,
            }

            restricted_globals = {
                "__builtins__": safe_builtins,
                "pd": pd,
                "np": np,
                "df": df,
                "emit_chunk": self.emit_chunk,
            }

            if file_ops:
                restricted_globals.update(
                    {
                        "open_read": file_ops.open_read,
                        "open_write": file_ops.open_write,
                        "read_text": file_ops.read_text,
                        "read_bytes": file_ops.read_bytes,
                        "write_text": file_ops.write_text,
                        "write_bytes": file_ops.write_bytes,
                        "read_csv": file_ops.read_csv,
                        "read_excel": file_ops.read_excel,
                        "read_json": file_ops.read_json,
                        "to_csv": file_ops.to_csv,
                        "to_excel": file_ops.to_excel,
                        "to_json": file_ops.to_json,
                        "list_input_files": file_ops.list_input_files,
                        "list_output_files": file_ops.list_output_files,
                        "file_exists": file_ops.file_exists,
                        "get_file_info": file_ops.get_file_info,
                        "INPUT_DIR": self.config.readonly_dir
                        if hasattr(self.config, "readonly_dir")
                        else "/data/input",
                        "OUTPUT_DIR": self.config.writable_dir
                        if hasattr(self.config, "writable_dir")
                        else "/data/output",
                    }
                )

            self.emit_chunk("status", {"message": "Executing code (restricted mode)"})

            exec(code, restricted_globals)

            execution_time = int((time.time() - start_time) * 1000)

            self.emit_chunk("status", {"message": "Execution completed"})

            return SandboxResult(
                success=True,
                output="Code executed successfully in restricted mode",
                execution_time_ms=execution_time,
            )

        except Exception as e:
            import traceback

            execution_time = int((time.time() - start_time) * 1000)
            error_trace = traceback.format_exc()

            self.emit_chunk("error", {"message": str(e), "traceback": error_trace})

            return SandboxResult(
                success=False,
                error=f"{type(e).__name__}: {str(e)}",
                execution_time_ms=execution_time,
                exit_code=1,
            )


# ---------------------------------------------------------------------------
# Sandbox sidecar backend (fase 3 of docs/SANDBOX_SIDECAR_API.md, contrato v1)
# ---------------------------------------------------------------------------

# Execution goes through the sandbox sidecar HTTP API when SANDBOX_API_URL is
# configured; empty keeps the legacy Docker path (backend "none") unchanged.
# The sidecar is the ONLY component that talks to the Docker daemon, so this
# client never touches a socket itself.
SIDECAR_ENV_URL = "SANDBOX_API_URL"
SIDECAR_ENV_TOKEN = "SANDBOX_API_TOKEN"

# §8 (obligatorio): the client's timeout must exceed the container timeout by
# a 15 s margin — the sidecar kills and cleans the container after its own
# timer expires, and the response only lands once that cleanup is done.
SIDECAR_HTTP_TIMEOUT_MARGIN_SECONDS = 15

# Availability probe timeout: long enough for a warm daemon, short enough not
# to stall the tool call when the sidecar is down.
SIDECAR_PROBE_TIMEOUT_SECONDS = 2.0


def sandbox_backend() -> str:
    """Return the execution backend: "sidecar" when SANDBOX_API_URL is set,
    else "none" (legacy Docker path). The decision happens before executing
    anything: with the sidecar configured there is never a docker or exec()
    fallback behind it (contract §13).
    """
    if os.environ.get(SIDECAR_ENV_URL, "").strip():
        return "sidecar"
    return "none"


def sidecar_reachable() -> tuple:
    """Probe the sidecar with GET /v1/health (§4.1); returns (ok, reason).

    The route is deliberately unauthenticated (container healthcheck, §3), so
    the probe sends no secret. Never raises — an unreachable sidecar is
    exactly what this function reports, and the caller logs the reason.
    """
    base_url = os.environ.get(SIDECAR_ENV_URL, "").strip().rstrip("/")
    if not base_url:
        return False, "SANDBOX_API_URL is not set"
    try:
        request = urllib.request.Request(f"{base_url}/v1/health", method="GET")
        with urllib.request.urlopen(
            request, timeout=SIDECAR_PROBE_TIMEOUT_SECONDS
        ) as response:
            status = json.loads(response.read().decode()).get("status")
    except urllib.error.HTTPError as exc:
        # 503 degraded: try to carry the sidecar's own status in the reason
        hint = ""
        try:
            hint = str(json.loads(exc.read().decode()).get("status") or "")
        except Exception:
            pass
        return False, (f"HTTP {exc.code} {hint}").rstrip()
    except Exception as exc:  # URLError, timeout, malformed body...
        return False, str(exc)
    if status == "ok":
        return True, ""
    return False, f"sidecar reports status={status!r} (degraded)"


def _elapsed_ms(start_time: float) -> int:
    return int((time.time() - start_time) * 1000)


def _memory_limit_mb(memory: Any) -> int:
    """Sidecar field ``memory_limit_mb`` (§4.3, an int) from the Docker-style
    SandboxConfig value ("512m"). Unrecognised formats keep the 512 default.
    """
    if isinstance(memory, (int, float)):
        return int(memory)
    text = str(memory).strip().lower()
    if text.isdigit():
        return int(text)
    if text.endswith("m") and text[:-1].isdigit():
        return int(text[:-1])
    if text.endswith("g") and text[:-1].isdigit():
        return int(text[:-1]) * 1024
    return 512


class SidecarSandboxedExecutor:
    """HTTP client of ``POST /v1/execute`` (contrato v1, §4.2/§4.3).

    Mirrors the API data_analysis already expects: ``execute(code,
    input_data)`` returns a SandboxResult whose ``to_dict()`` carries the
    §4.4 contract fields; the extra response fields (request_id, error_code,
    output_truncated, ...) are additive and ignored here. Sidecar errors
    (§6.1 → §6.2) are mapped to SandboxResult, never raised, and there is NO
    client-side retry (§9).

    ``input_data`` rides inline as vía A (``format: "records"``); res://
    resources are named by whoever builds the request — the sidecar resolves
    them (§5, regla 1), this client never opens the file behind a URI.
    """

    CHUNK_CALLBACK = None

    def __init__(self, config: Optional[SandboxConfig] = None):
        self.config = config or SandboxConfig()
        self._base_url = os.environ.get(SIDECAR_ENV_URL, "").strip().rstrip("/")
        self._token = os.environ.get(SIDECAR_ENV_TOKEN, "").strip()

    def set_chunk_callback(self, callback: Callable[[dict], None]):
        SidecarSandboxedExecutor.CHUNK_CALLBACK = callback

    def emit_chunk(self, chunk_type: str, data: Any):
        chunk = {"type": chunk_type, "data": data}
        if SidecarSandboxedExecutor.CHUNK_CALLBACK:
            SidecarSandboxedExecutor.CHUNK_CALLBACK(chunk)
        return chunk

    def _payload(self, code: str, input_data=None) -> dict:
        """§4.3 request body: limits from SandboxConfig, input_data as records."""
        payload: Dict[str, Any] = {"code": code}
        if input_data is not None:
            records = input_data if isinstance(input_data, list) else [input_data]
            payload["input_data"] = {
                "format": "records",
                "name": "input.csv",
                "records": records,
            }
        payload["timeout_seconds"] = int(self.config.timeout)
        payload["memory_limit_mb"] = _memory_limit_mb(self.config.memory)
        if self.config.cpu is not None:
            payload["cpu_limit"] = float(self.config.cpu)
        if self.config.pids is not None:
            payload["pids_limit"] = int(self.config.pids)
        return payload

    def execute(
        self,
        code: str,
        input_data: Optional[Union[Dict[str, Any], List[Any]]] = None,
        env_vars: Optional[Dict[str, str]] = None,
    ) -> SandboxResult:
        start_time = time.time()
        self.emit_chunk("status", {"message": "Executing code via sandbox sidecar"})

        if not self._token:
            # Fail closed BEFORE the call: /v1/execute is Bearer-guarded (§3),
            # so a header-less request could only earn a 401. Without the
            # secret there is no usable sidecar backend at all.
            return SandboxResult(
                success=False,
                error="Sandbox service unavailable: SANDBOX_API_TOKEN is not set "
                "for the sidecar call (contract §3)",
                execution_time_ms=_elapsed_ms(start_time),
            )

        body = json.dumps(self._payload(code, input_data), default=str).encode()
        request = urllib.request.Request(
            f"{self._base_url}/v1/execute",
            data=body,
            method="POST",
            headers={
                "Authorization": f"Bearer {self._token}",
                "Content-Type": "application/json; charset=utf-8",
                # §9: the client does not retry, but a per-request key keeps
                # any accidental transport-level double-send safe (same body
                # ⇒ cached reply, different body ⇒ IDEMPOTENCY_CONFLICT).
                "Idempotency-Key": uuid.uuid4().hex,
            },
        )
        try:
            with urllib.request.urlopen(
                request,
                timeout=int(self.config.timeout) + SIDECAR_HTTP_TIMEOUT_MARGIN_SECONDS,
            ) as response:
                payload = json.loads(response.read().decode())
        except urllib.error.HTTPError as exc:
            return self._map_http_error(exc, start_time)
        except Exception as exc:  # URLError, socket timeout, refused connection...
            self.emit_chunk("error", {"message": f"Sandbox service unavailable: {exc}"})
            return SandboxResult(
                success=False,
                error=f"Sandbox service unavailable: {exc}",
                execution_time_ms=_elapsed_ms(start_time),
            )

        chunks = payload.get("chunks") or []
        for chunk in chunks:
            self.emit_chunk(chunk.get("type", "unknown"), chunk.get("data", {}))

        return SandboxResult(
            success=bool(payload.get("success")),
            output=payload.get("output") or "",
            error=payload.get("error"),
            chunks=chunks,
            files=payload.get("files") or {},
            # The container-side measurement (§4.4) wins over the client's own
            # clock; fall back to elapsed time if the field is absent.
            execution_time_ms=(
                int(payload["execution_time_ms"])
                if payload.get("execution_time_ms") is not None
                else _elapsed_ms(start_time)
            ),
            exit_code=int(payload.get("exit_code", 0)),
        )

    def _map_http_error(
        self, exc: "urllib.error.HTTPError", start_time: float
    ) -> SandboxResult:
        """Map a non-200 answer to SandboxResult (§6.1 → §6.2). No retries."""
        try:
            payload = json.loads(exc.read().decode())
        except Exception:
            payload = {}
        if not isinstance(payload, dict):
            payload = {}
        error = payload.get("error")
        detail = error.get("message") if isinstance(error, dict) else ""

        # 400/413/409 are request-contract problems (integration bugs, §6.2):
        # the sidecar's message names the offending field, so keep it verbatim.
        if exc.code in (400, 413, 409):
            self.emit_chunk(
                "error",
                {"message": f"Sidecar rejected the request: {detail or exc.code}"},
            )
            return SandboxResult(
                success=False,
                error=detail or f"Sidecar error HTTP {exc.code}",
                execution_time_ms=_elapsed_ms(start_time),
            )

        # 401/429/500/502/503 and anything else: the execution service is not
        # usable right now — consistent with what the probe reports (§6.2).
        self.emit_chunk(
            "error", {"message": f"Sidecar error HTTP {exc.code}: {detail}"}
        )
        return SandboxResult(
            success=False,
            error=f"Sandbox service unavailable: HTTP {exc.code} {detail}".rstrip(),
            execution_time_ms=_elapsed_ms(start_time),
        )


def execute_in_sandbox(
    code: str,
    input_data: Optional[Union[Dict[str, Any], List[Any]]] = None,
    config: Optional[SandboxConfig] = None,
    on_chunk: Optional[Callable[[dict], None]] = None,
) -> SandboxResult:
    if sandbox_backend() == "sidecar":
        # SANDBOX_API_URL set: the sidecar executes and there is NO docker or
        # exec() fallback behind it (§13) — every failure maps to the result.
        sidecar_executor = SidecarSandboxedExecutor(config)
        if on_chunk:
            sidecar_executor.set_chunk_callback(on_chunk)
        return sidecar_executor.execute(code, input_data)

    executor = DockerSandboxedExecutor(config)
    if on_chunk:
        executor.set_chunk_callback(on_chunk)

    if DOCKER_AVAILABLE:
        return executor.execute(code, input_data)
    else:
        sandbox_required = os.environ.get("SANDBOX_REQUIRED", "false").lower() == "true"
        if sandbox_required:
            return SandboxResult(
                success=False,
                error="Docker sandbox is required but not available (SANDBOX_REQUIRED=true). "
                      "Code execution denied for security. Install and start Docker, then try again.",
                execution_time_ms=0,
            )
        return executor.execute_fallback(code, input_data)
