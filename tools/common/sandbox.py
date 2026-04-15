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
        memory_limit: str = "512m",
        cpu_limit: float = 0.5,
        pids_limit: int = 50,
        network_disabled: bool = True,
        build_on_missing: bool = True,
        readonly_dir: str = "/data/input",
        writable_dir: str = "/data/output",
        max_file_size_mb: int = 100,
    ):
        self.image = image
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
                except:
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

input_data = None
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

        ALLOWED_ATTRIBUTE_ACCESS = {
            "delattr",
            "setattr",
            "getattr",
            "hasattr",
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
        input_data: Optional[Dict[str, Any]] = None,
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

            restricted_globals = {
                "__builtins__": safe_builtins,
                "pd": pd,
                "np": np,
                "df": df,
                "emit_chunk": self.emit_chunk,
            }

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
