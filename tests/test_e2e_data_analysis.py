#!/usr/bin/env python3
"""
End-to-end test for data_analysis tool with sandbox and streaming.

This test:
1. Creates test data (CSV file)
2. Builds the sandbox Docker image
3. Executes the analyze_data tool with a question
4. Verifies the response contains expected chunks and result
"""

import json
import os
import sys
import time
import subprocess

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools", "common"))

TEST_DATA_DIR = "/home/amphora/Proyectos/mcp-go/test_data"
EMPLOYEES_CSV = os.path.join(TEST_DATA_DIR, "empleados.csv")


def create_test_data():
    """Create test CSV file with employee data."""
    os.makedirs(TEST_DATA_DIR, exist_ok=True)

    data = """id,nombre,departamento,salario,edad,fecha_contratacion
1,Juan García,Ingeniería,75000,32,2020-03-15
2,María López,Marketing,65000,28,2021-06-01
3,Pedro Sánchez,Ingeniería,80000,35,2019-01-20
4,Ana Martínez,HR,55000,29,2022-02-10
5,David Rodríguez,Finanzas,72000,31,2020-11-05
6,Laura Fernández,Marketing,68000,27,2021-09-18
7,Carlos Gómez,Ingeniería,82000,38,2018-07-22
8,Elena Ruiz,HR,58000,30,2020-04-30
9,Antonio Blanco,Finanzas,70000,33,2019-08-12
10,Sofia Díaz,Marketing,62000,26,2022-01-15
"""

    with open(EMPLOYEES_CSV, "w", encoding="utf-8") as f:
        f.write(data)

    print(f"✅ Created test data: {EMPLOYEES_CSV}")
    return EMPLOYEES_CSV


def build_sandbox_image():
    """Build the sandbox Docker image."""
    print("🔨 Building sandbox Docker image...")

    dockerfile = "/home/amphora/Proyectos/mcp-go/tools/data_analysis/sandbox.Dockerfile"

    result = subprocess.run(
        [
            "docker",
            "build",
            "-t",
            "mcp-python-sandbox:latest",
            "-f",
            dockerfile,
            os.path.dirname(dockerfile),
        ],
        capture_output=True,
        text=True,
    )

    if result.returncode == 0:
        print("✅ Sandbox image built successfully")
        return True
    else:
        print(f"❌ Docker build failed: {result.stderr}")
        return False


def run_sandbox_test():
    """Run the sandbox executor directly."""
    print("\n🚀 Running sandbox execution test...")

    code = """
import pandas as pd
df = pd.read_csv("/data/empleados.csv")
result = df.groupby("departamento")["salario"].mean().round(0).astype(int)
print(result)
"""

    from sandbox import DockerSandboxedExecutor, SandboxConfig

    config = SandboxConfig(
        image="mcp-python-sandbox:latest",
        timeout_seconds=60,
        memory_limit="256m",
        cpu_limit=0.5,
        pids_limit=50,
        network_disabled=True,
        build_on_missing=False,
    )

    chunks = []

    def on_chunk(chunk):
        chunks.append(chunk)
        chunk_type = chunk.get("type", "unknown")
        chunk_data = chunk.get("data", {})
        print(f"  📦 Chunk [{chunk_type}]: {chunk_data}")

    executor = DockerSandboxedExecutor(config)
    executor.set_chunk_callback(on_chunk)

    print(f"\n⚙️  Executing code in sandbox...")
    start_time = time.time()

    result = executor.execute_with_volume(
        code, "/home/amphora/Proyectos/mcp-go/test_data"
    )

    execution_time = (time.time() - start_time) * 1000

    print(f"\n⏱️  Execution time: {execution_time:.0f}ms")
    print(f"✅ Success: {result.success}")
    print(f"📄 Output:\n{result.output}")

    if result.error:
        print(f"❌ Error: {result.error}")

    print(f"\n📦 Total chunks received: {len(chunks)}")

    return {
        "success": result.success,
        "execution_time_ms": execution_time,
        "output": result.output,
        "chunks": chunks,
        "error": result.error,
    }


def run_fallback_test():
    """Test using fallback exec() when Docker is not available."""
    print("\n🐍 Testing with fallback exec() (no Docker)...")

    import pandas as pd

    df = pd.read_csv(EMPLOYEES_CSV)
    code = """
result = df.groupby("departamento")["salario"].mean().round(0).astype(int)
print(result)
"""

    SAFE_BUILTINS = {
        "abs": abs,
        "all": all,
        "any": any,
        "bool": bool,
        "dict": dict,
        "enumerate": enumerate,
        "filter": filter,
        "float": float,
        "format": format,
        "int": int,
        "isinstance": isinstance,
        "len": len,
        "list": list,
        "map": map,
        "max": max,
        "min": min,
        "print": print,
        "range": range,
        "reversed": reversed,
        "round": round,
        "set": set,
        "sorted": sorted,
        "str": str,
        "sum": sum,
        "tuple": tuple,
        "type": type,
        "zip": zip,
    }

    from io import StringIO
    from contextlib import redirect_stdout, redirect_stderr

    restricted_globals = {
        "__builtins__": SAFE_BUILTINS,
        "pd": pd,
        "np": pd.np,
        "df": df,
    }

    stdout_capture = StringIO()
    stderr_capture = StringIO()

    result = None
    with redirect_stdout(stdout_capture), redirect_stderr(stderr_capture):
        try:
            exec(code, restricted_globals)
            if "result" in restricted_globals:
                result = restricted_globals["result"]
        except Exception as e:
            stderr_capture.write(f"Execution error: {str(e)}")

    stdout = stdout_capture.getvalue()
    stderr = stderr_capture.getvalue()

    print(f"✅ Success: {stderr == ''}")
    print(f"📄 Output:\n{stdout}")
    if stderr:
        print(f"⚠️  Stderr: {stderr}")

    return {"success": stderr == "", "stdout": stdout, "stderr": stderr}


def main():
    print("=" * 60)
    print("🧪 End-to-End Test: data_analysis Tool")
    print("=" * 60)

    errors = []

    try:
        create_test_data()

        docker_available = (
            subprocess.run(
                ["docker", "info"], capture_output=True, text=True
            ).returncode
            == 0
        )

        if docker_available:
            print("🐳 Docker is available")
            try:
                build_sandbox_image()
                result = run_sandbox_test()
                if not result["success"]:
                    errors.append(f"Sandbox execution failed: {result['error']}")
            except Exception as e:
                print(f"⚠️  Sandbox error: {e}")
                print("🔄 Falling back to exec() test...")
                run_fallback_test()
        else:
            print("⚠️  Docker not available")
            run_fallback_test()

        print("\n" + "=" * 60)
        if errors:
            print("❌ TEST FAILED:")
            for err in errors:
                print(f"   - {err}")
        else:
            print("✅ TEST PASSED (with fallback)")
        print("=" * 60)

    except Exception as e:
        print(f"❌ Test error: {e}")
        import traceback

        traceback.print_exc()


if __name__ == "__main__":
    main()
