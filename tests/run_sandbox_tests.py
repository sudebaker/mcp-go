#!/usr/bin/env python3
"""Run the sandbox tests."""
import subprocess
import sys

# Check if pytest is available via pip3 install --break-system-packages
result = subprocess.run(
    [sys.executable, "-m", "pytest", "--version"],
    capture_output=True, text=True
)
if result.returncode != 0:
    print("pytest not found, trying to install...")
    result = subprocess.run(
        ["pip3", "install", "--break-system-packages", "pytest"],
        capture_output=True, text=True
    )
    print(result.stdout[-500:] if result.stdout else "")
    print(result.stderr[-500:] if result.stderr else "")

result = subprocess.run(
    [sys.executable, "-m", "pytest", "tests/test_sandbox.py", "-v"],
    capture_output=True, text=True, cwd="/home/amphora/src/mcp-go"
)
print(result.stdout)
if result.stderr:
    print("STDERR:", result.stderr)
sys.exit(result.returncode)
