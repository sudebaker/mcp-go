#!/usr/bin/env python3
"""Tests for transcribe's scratch directory (t_1b5e1bdb).

transcribe_base64 used to write the decoded audio into ``/data/tmp`` — a
directory that never existed; sandbox jobs mount /data read-only. The temp
file now lands in ``scratch_dir()``: OUTPUT_DIR, injected by the sidecar
engine and the in-tool docker executor, falling back to
SANDBOX_OUTPUT_DIR. The file is unlinked after transcription, so the test
asserts inside the (stubbed) transcription call.
"""

import base64
import importlib.util
import sys
from pathlib import Path

import pytest

TOOL_DIR = Path(__file__).resolve().parent
MODULE_NAME = "transcribe_main_under_test"


def load_tool():
    """Load tools/transcribe/main.py — only stdlib + common.* imports."""
    sys.path.insert(0, str(TOOL_DIR.parent))
    spec = importlib.util.spec_from_file_location(MODULE_NAME, TOOL_DIR / "main.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


AUDIO_B64 = base64.b64encode(b"RIFF-fake-wav-audio").decode()


def test_scratch_dir_defaults_to_the_sandbox_mount(monkeypatch):
    module = load_tool()

    monkeypatch.delenv("OUTPUT_DIR", raising=False)
    assert module.SANDBOX_OUTPUT_DIR == "/tmp/output"
    assert module.scratch_dir() == Path("/tmp/output")

    monkeypatch.setenv("OUTPUT_DIR", "/tmp/job-output")
    assert module.scratch_dir() == Path("/tmp/job-output"), (
        "the executor/engine-injected env must win over the constant"
    )


def test_decoded_audio_is_written_inside_sandbox_output_dir(monkeypatch, tmp_path):
    module = load_tool()
    monkeypatch.setenv("OUTPUT_DIR", str(tmp_path))

    recorded = []

    def fake_transcribe_file(path, language=None):
        recorded.append(Path(path))
        # the unlink happens in transcribe_base64's finally: while the
        # transcription runs the file MUST exist and be the injected mount
        assert Path(path).is_file(), f"temp audio missing during transcription: {path}"
        assert str(tmp_path) in str(path), f"temp audio outside the sandbox output dir: {path}"
        return "transcript", None

    monkeypatch.setattr(module, "transcribe_file", fake_transcribe_file)

    text, error = module.transcribe_base64(AUDIO_B64, "fake.wav")

    assert error is None and text == "transcript", (text, error)
    assert len(recorded) == 1
    assert recorded[0].suffix == ".wav"

    # transcribe_base64 promised cleanup of its own temp file
    assert not recorded[0].exists(), "the temp audio file should be unlinked after use"


def test_output_dir_is_created_on_demand(monkeypatch, tmp_path):
    module = load_tool()
    fresh = tmp_path / "does-not-exist-yet"
    monkeypatch.setenv("OUTPUT_DIR", str(fresh))

    module.scratch_dir().mkdir(parents=True, exist_ok=True)

    assert fresh.is_dir(), "scratch_dir() must be creatable by the caller"


if __name__ == "__main__":
    sys.exit(pytest.main([__file__, "-v"]))