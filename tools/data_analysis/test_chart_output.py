#!/usr/bin/env python3
"""
Regression tests for chart generation inside analyze_data (t_14685e1f).

The bug in one line: the visualization prompt ordered the model to save to
`/data/output/chart.png` — the pre-sidecar harness mount — while the sandbox
container has /data read-only and its rw output mount at /tmp/output
(internal/sidecar/engine.go OUTPUT_DIR=/tmp/output). Every chart request
died with `[Errno 2] No such file or directory: '/data/output/chart.png'`.

The fix, pinned here:

1. `SafeFileOperations.save_figure` writes a matplotlib figure into the
   writable dir with the class's own traversal/size protections (no BytesIO
   in generated code: `io` is off the sandbox whitelist);
2. the saving rules are ALWAYS present in the prompt and reference only
   SANDBOX_OUTPUT_DIR / save_figure — the prompt can never drift back to a
   hardcoded /data/output path;
3. the real bootstrap (container harness) renders an actual PNG through
   safe_files.save_figure with the image's own matplotlib — run in Docker
   when the sandbox image is available, skipped otherwise.
"""

import base64
import importlib.util
import os
import shutil
import subprocess
import sys
from pathlib import Path

import pytest

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_HERE, ".."))

from common.safe_file_ops import SafeFileOperations, SANDBOX_OUTPUT_DIR  # noqa: E402
from common.validators import PathValidationError  # noqa: E402


class _FakeFigure:
    """A Figure stand-in: savefig writes bytes, exactly what the harness needs."""

    def __init__(self, payload: bytes = b"\x89PNG-fake-bytes"):
        self.payload = payload

    def savefig(self, path, dpi=None, **kwargs):
        with open(path, "wb") as fh:
            fh.write(self.payload)


class _FakePyplot:
    """Stands in for the pyplot module an LLM payload is likely to pass."""

    def __init__(self, fig: _FakeFigure):
        self._fig = fig

    def gcf(self):
        return self._fig


# --- 1. save_figure: unit behaviour (stdlib-only, no matplotlib needed) ----


def _ops(tmp_path: Path, **kwargs) -> SafeFileOperations:
    return SafeFileOperations(
        readonly_dir=str(tmp_path / "input"),
        writable_dir=str(tmp_path / "output"),
        **kwargs,
    )


def test_save_figure_writes_inside_the_writable_dir(tmp_path):
    ops = _ops(tmp_path)
    (tmp_path / "output").mkdir()

    path = ops.save_figure(_FakeFigure(), "chart.png")

    assert path.startswith(str(tmp_path / "output"))
    assert Path(path).read_bytes() == b"\x89PNG-fake-bytes"
    assert Path(path).name == "chart.png"


def test_save_figure_defaults_to_png_suffix(tmp_path):
    ops = _ops(tmp_path)
    (tmp_path / "output").mkdir()

    path = ops.save_figure(_FakeFigure(), "chart")

    assert Path(path).suffix == ".png"
    assert Path(path).exists()


def test_save_figure_accepts_the_pyplot_module(tmp_path):
    ops = _ops(tmp_path)
    (tmp_path / "output").mkdir()

    path = ops.save_figure(_FakePyplot(_FakeFigure()), "chart.png")

    assert Path(path).exists()


def test_save_figure_rejects_relative_traversal(tmp_path):
    ops = _ops(tmp_path)
    (tmp_path / "output").mkdir()

    with pytest.raises(PathValidationError):
        ops.save_figure(_FakeFigure(), "../escape.png")

    # Nothing landed outside the writable dir.
    assert not (tmp_path / "escape.png").exists()


def test_save_figure_rejects_absolute_outside_path(tmp_path):
    ops = _ops(tmp_path)
    (tmp_path / "output").mkdir()

    with pytest.raises(PathValidationError):
        ops.save_figure(_FakeFigure(), "/etc/steal.png")

    assert not Path("/etc/steal.png").exists()


def test_save_figure_enforces_the_size_limit_and_cleans_up(tmp_path):
    ops = _ops(tmp_path, max_file_size_mb=1)
    (tmp_path / "output").mkdir()

    big = _FakeFigure(payload=b"x" * (2 * 1024 * 1024))
    with pytest.raises(ValueError, match="too large"):
        ops.save_figure(big, "chart.png")

    # The oversized file must not linger for the sidecar to collect.
    assert list((tmp_path / "output").glob("*")) == []


def test_save_figure_rejects_non_figure_objects(tmp_path):
    ops = _ops(tmp_path)
    (tmp_path / "output").mkdir()

    with pytest.raises(TypeError):
        ops.save_figure("not a figure", "chart.png")


# --- 2. the sandbox output constant is the real mount ----------------------


def test_sandbox_output_constant_is_the_sidecar_mount():
    """The sidecar engine (internal/sidecar/engine.go) mounts /tmp/output."""
    assert SANDBOX_OUTPUT_DIR == "/tmp/output"


def test_default_writable_dir_is_the_real_mount():
    ops = SafeFileOperations()
    assert ops.writable_dir == SANDBOX_OUTPUT_DIR


# --- 3. prompt anti-drift: no absolute /data/output path, rules always -----


def _load_main():
    main_path = os.path.join(_HERE, "main.py")
    spec = importlib.util.spec_from_file_location("data_analysis_main_charts", main_path)
    if spec is None or spec.loader is None:  # pragma: no cover
        raise RuntimeError(f"cannot import {main_path}: no module spec")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.fixture(scope="module")
def main():
    return _load_main()


@pytest.mark.parametrize("output_format", ["image", "text", "json", "markdown"])
def test_prompt_never_carries_a_hardcoded_data_output_path(main, output_format):
    """The prompt must not contain any literal /data/output absolute path."""
    prompt = main.build_analysis_prompt("SUMMARY", "question", output_format)

    assert "/data/output" not in prompt
    # The rules point at the real mount through the constant instead.
    assert SANDBOX_OUTPUT_DIR in prompt


@pytest.mark.parametrize("output_format", ["image", "text", "json", "markdown"])
def test_prompt_save_rules_are_always_present(main, output_format):
    """
    Saving rules exist regardless of the enum: a natural-language
    "draw a chart" request keeps output_format=text and still saves.
    """
    prompt = main.build_analysis_prompt("SUMMARY", "question", output_format)

    assert "safe_files.save_figure" in prompt
    assert "plt.close()" in prompt
    assert "Agg" in prompt
    assert "Never call plt.savefig()" in prompt


def test_image_format_adds_the_visualization_requirement(main):
    prompt = main.build_analysis_prompt("SUMMARY", "question", "image")
    assert "VISUALIZATION REQUIRED" in prompt


# --- 4. harness test with real matplotlib (Docker, when available) ---------


def _sandbox_image_available() -> bool:
    """True when Docker works AND the sandbox image exists locally."""
    docker = shutil.which("docker")
    if docker is None:
        return False
    probe = subprocess.run(
        [docker, "info", "--format", "{{.ServerVersion}}"],
        capture_output=True,
        text=True,
        timeout=30,
    )
    if probe.returncode != 0:
        return False
    image = os.environ.get("SANDBOX_IMAGE", "mcp-python-sandbox:latest")
    check = subprocess.run(
        [docker, "image", "inspect", image],
        capture_output=True,
        text=True,
        timeout=30,
    )
    return check.returncode == 0


@pytest.mark.skipif(
    not _sandbox_image_available(),
    reason="Docker daemon or mcp-python-sandbox image not available",
)
def test_bootstrap_renders_a_real_png_with_the_image_matplotlib(tmp_path):
    """
    Run the REAL bootstrap inside the REAL sandbox image: the LLM-style chart
    payload (plt -> safe_files.save_figure -> plt.close) must leave a PNG in
    the collected output dir (files >= 1).
    """
    image = os.environ.get("SANDBOX_IMAGE", "mcp-python-sandbox:latest")

    input_dir = tmp_path / "input"
    output_dir = tmp_path / "output"
    (input_dir / "input.csv").parent.mkdir(parents=True)
    (input_dir / "input.csv").write_text("a,b\n1,2\n3,4\n")
    output_dir.mkdir()
    os.chmod(str(output_dir), 0o777)  # image runs as a non-root user
    os.chmod(str(input_dir), 0o755)

    code = (
        "import matplotlib.pyplot as plt\n"
        "fig, ax = plt.subplots()\n"
        "ax.bar(['a', 'b'], [1, 3])\n"
        "safe_files.save_figure(plt.gcf(), 'chart.png')\n"
        "plt.close()\n"
        "print('chart saved')\n"
    )

    env = [
        "-e",
        "MCP_SANDBOX_CODE=" + base64.b64encode(code.encode()).decode(),
        "-e",
        "MPLCONFIGDIR=/tmp/matplotlib",
        "-e",
        f"OUTPUT_DIR={SANDBOX_OUTPUT_DIR}",  # what the executor injects
        "-e",
        "INPUT_DIR=/data/input",
    ]

    result = subprocess.run(
        [
            "docker",
            "run",
            "--rm",
            "--network",
            "none",
            "-v",
            f"{input_dir}:/data/input:ro",
            "-v",
            f"{output_dir}:/tmp/output:rw",
            *env,
            image,
            "python3",
            "/sandbox/sandbox_bootstrap.py",
        ],
        capture_output=True,
        text=True,
        timeout=180,
    )

    assert result.returncode == 0, result.stdout + result.stderr
    assert "__RESULT__" in result.stdout
    pngs = list(output_dir.glob("*.png"))
    assert len(pngs) >= 1, f"no PNG collected (files<1): {result.stdout}"
    assert pngs[0].stat().st_size > 0