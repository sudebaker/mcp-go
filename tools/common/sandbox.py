#!/usr/bin/env python3
"""
Docker-based sandbox executor for secure code execution.

Provides isolated execution environments with resource limits.
Container is always removed after execution (auto_remove=True).
"""

import json
import os
import sys
import time
import uuid
from typing import Any, Callable, Dict, Optional

try:
    import docker
    from docker.errors import APIError, ContainerError

    DOCKER_AVAILABLE = True
except ImportError:
    DOCKER_AVAILABLE = False


CHUNK_PREFIX = "__CHUNK__:"
RESULT_PREFIX = "__RESULT__:"


class SandboxConfig:
    def __init__(
        self,
        image: str = "mcp-python-sandbox:latest",
        timeout_seconds: int = 60,
        memory_limit: str = "256m",
        cpu_limit: float = 0.5,
        pids_limit: int = 50,
        network_disabled: bool = True,
        build_on_missing: bool = True,
    ):
        self.image = image
        self.timeout = timeout_seconds
        self.memory = memory_limit
        self.cpu = cpu_limit
        self.pids = pids_limit
        self.network_disabled = network_disabled
        self.build_on_missing = build_on_missing


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

    def build_image(self, dockerfile_path: str, tag: Optional[str] = None) -> bool:
        tag = tag or self.config.image
        try:
            self.client.images.build(
                path=os.path.dirname(dockerfile_path),
                dockerfile=os.path.basename(dockerfile_path),
                tag=tag,
                rm=True,
            )
            return True
        except APIError as e:
            self.emit_chunk("error", {"message": f"Failed to build image: {e}"})
            return False

    def _ensure_image_exists(self) -> bool:
        try:
            self.client.images.get(self.config.image)
            return True
        except APIError:
            if self.config.build_on_missing:
                dockerfile = os.path.join(
                    os.path.dirname(os.path.dirname(os.path.dirname(__file__))),
                    "tools",
                    "data_analysis",
                    "sandbox.Dockerfile",
                )
                if os.path.exists(dockerfile):
                    return self.build_image(dockerfile)
            return False

    def execute(
        self,
        code: str,
        input_data: Optional[Dict[str, Any]] = None,
        env_vars: Optional[Dict[str, str]] = None,
    ) -> SandboxResult:
        start_time = time.time()
        container = None

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
                from io import StringIO

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

            encoded_code = base64.b64encode(code.encode()).decode()

            volumes = {
                host_path: {"bind": "/data", "mode": "ro"},
            }

            env = env_vars.copy() if env_vars else {}
            env["MCP_SANDBOX_CODE"] = encoded_code

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

input_data = None
if "INPUT_PLACEHOLDER":
    try:
        csv_data = base64.b64decode("INPUT_PLACEHOLDER").decode()
        input_data = pd.read_csv(StringIO(csv_data))
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
    "input_data": input_data,
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

    def execute_fallback(
        self,
        code: str,
        input_data: Optional[Dict[str, Any]] = None,
        on_chunk: Optional[Callable[[dict], None]] = None,
    ) -> SandboxResult:
        if on_chunk:
            self.set_chunk_callback(on_chunk)

        self.emit_chunk("warning", {"message": "Using unsafe exec() fallback"})

        start_time = time.time()

        try:
            import pandas as pd

            df = None
            if input_data is not None:
                df = (
                    pd.DataFrame(input_data)
                    if isinstance(input_data, list)
                    else pd.DataFrame([input_data])
                )

            restricted_globals = {
                "_builtins_": {
                    "print": print,
                    "len": len,
                    "str": str,
                    "int": int,
                    "float": float,
                    "list": list,
                    "dict": dict,
                    "range": range,
                    "abs": abs,
                    "max": max,
                    "min": min,
                    "sum": sum,
                    "sorted": sorted,
                    "zip": zip,
                    "map": map,
                    "filter": filter,
                    "open": None,
                    "__import__": None,
                    "exec": None,
                    "eval": None,
                },
                "pd": pd,
                "input_data": df,
                "emit_chunk": self.emit_chunk,
            }

            self.emit_chunk("status", {"message": "Executing code (unsafe mode)"})

            exec(code, restricted_globals)

            execution_time = int((time.time() - start_time) * 1000)

            self.emit_chunk("status", {"message": "Execution completed"})

            return SandboxResult(
                success=True,
                output="Code executed",
                execution_time_ms=execution_time,
            )

        except Exception as e:
            import traceback

            execution_time = int((time.time() - start_time) * 1000)
            self.emit_chunk(
                "error", {"message": str(e), "traceback": traceback.format_exc()}
            )

            return SandboxResult(
                success=False,
                error=str(e),
                execution_time_ms=execution_time,
            )


def execute_in_sandbox(
    code: str,
    input_data: Optional[Dict[str, Any]] = None,
    config: Optional[SandboxConfig] = None,
    on_chunk: Optional[Callable[[dict], None]] = None,
) -> SandboxResult:
    executor = DockerSandboxedExecutor(config)
    if on_chunk:
        executor.set_chunk_callback(on_chunk)

    if DOCKER_AVAILABLE:
        return executor.execute(code, input_data)
    else:
        return executor.execute_fallback(code, input_data)
