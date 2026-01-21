#!/usr/bin/env python3
"""
Tests for the sandbox module.
"""

import json
import sys
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import pytest
from unittest.mock import patch, MagicMock

from common.sandbox import (
    SandboxConfig,
    SandboxResult,
    DockerSandboxedExecutor,
    execute_in_sandbox,
    CHUNK_PREFIX,
    RESULT_PREFIX,
)


class TestSandboxConfig:
    def test_default_config(self):
        config = SandboxConfig()
        assert config.image == "mcp-python-sandbox:latest"
        assert config.timeout == 60
        assert config.memory == "256m"
        assert config.cpu == 0.5
        assert config.pids == 50
        assert config.network_disabled is True

    def test_custom_config(self):
        config = SandboxConfig(
            image="custom-image:latest",
            timeout=120,
            memory="512m",
            cpu=1.0,
            pids=100,
            network_disabled=False,
        )
        assert config.image == "custom-image:latest"
        assert config.timeout == 120
        assert config.memory == "512m"
        assert config.cpu == 1.0
        assert config.pids == 100
        assert config.network_disabled is False


class TestSandboxResult:
    def test_successful_result(self):
        result = SandboxResult(
            success=True,
            output="test output",
            chunks=[{"type": "status", "data": {"message": "done"}}],
            execution_time_ms=100,
        )
        assert result.success is True
        assert result.output == "test output"
        assert len(result.chunks) == 1
        assert result.execution_time_ms == 100

    def test_failed_result(self):
        result = SandboxResult(
            success=False,
            error="execution failed",
            execution_time_ms=50,
        )
        assert result.success is False
        assert result.error == "execution failed"

    def test_to_dict(self):
        result = SandboxResult(
            success=True,
            output="output",
            chunks=[{"type": "progress", "data": 50}],
            files={"chart.png": "deadbeef"},
            execution_time_ms=100,
            exit_code=0,
        )
        d = result.to_dict()
        assert d["success"] is True
        assert d["output"] == "output"
        assert len(d["chunks"]) == 1
        assert "chart.png" in d["files"]


class TestDockerSandboxedExecutor:
    def test_executor_init(self):
        executor = DockerSandboxedExecutor()
        assert executor.config.image == "mcp-python-sandbox:latest"

    def test_emit_chunk(self):
        executor = DockerSandboxedExecutor()
        chunks = []

        def capture(chunk):
            chunks.append(chunk)

        executor.set_chunk_callback(capture)
        executor.emit_chunk("status", {"message": "test"})

        assert len(chunks) == 1
        assert chunks[0]["type"] == "status"
        assert chunks[0]["data"]["message"] == "test"


class TestExecuteInSandbox:
    @patch("common.sandbox.DOCKER_AVAILABLE", False)
    def test_fallback_execution(self):
        result = execute_in_sandbox("print('hello')")
        assert result.success is True
        assert result.error is None

    @patch("common.sandbox.DOCKER_AVAILABLE", False)
    def test_fallback_with_error(self):
        result = execute_in_sandbox("raise Exception('test error')")
        assert result.success is False
        assert "test error" in result.error


class TestChunkProtocol:
    def test_chunk_prefix(self):
        assert CHUNK_PREFIX == "__CHUNK__:"

    def test_result_prefix(self):
        assert RESULT_PREFIX == "__RESULT__:"

    def test_chunk_format(self):
        chunk = {"type": "status", "data": {"message": "loading"}}
        line = CHUNK_PREFIX + json.dumps(chunk)
        assert line == '__CHUNK__:{"type": "status", "data": {"message": "loading"}}'

    def test_result_format(self):
        result = {"success": True, "output": "done"}
        line = RESULT_PREFIX + json.dumps(result)
        assert line == '__RESULT__:{"success": true, "output": "done"}'


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
