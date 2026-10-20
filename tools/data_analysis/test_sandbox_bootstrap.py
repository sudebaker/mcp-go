#!/usr/bin/env python3
"""
Regression tests for the sandbox import gate (t_52311b49; contract
docs/SANDBOX_SIDECAR_API.md §7.1).

The evidence chain of the bug: analyze_data's LLM generates code that starts
with `import pandas as pd`, but both harnesses executed user code with
restricted builtins that omitted `__import__` (bootstrap had it commented
out; the in-process fallback never bound it) — every run died with
NameError "__import__ not found" as soon as the engine stopped killing the
job early (PR #68).

The fix is one shared whitelist gate (`tools/common/safe_import.py`) bound
as `__import__` in both harnesses. Pinned here:

1. the criterion payload `import pandas as pd ... print(len(df))` exits 0
   through the real bootstrap process (pandas/numpy/matplotlib stubbed via
   PYTHONPATH so the suite runs in the slim CI env without them);
2. forbidden modules (os/sys/subprocess/socket) and relative imports are
   denied with a clear ImportError, nothing of the code runs;
3. the in-process fallback (execute_code_safely) obeys the same gate.
"""

import base64
import importlib.util
import os
import subprocess
import sys

import pytest

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_HERE, ".."))

from common.safe_import import SandboxImportError, sandbox_import  # noqa: E402

_BOOTSTRAP = os.path.join(_HERE, "sandbox_bootstrap.py")


# --- shared gate (tools/common/safe_import.py), stdlib-only assertions ----


def test_sandbox_import_admits_whitelisted_roots():
    import collections
    import collections.abc
    import json

    assert sandbox_import("json") is json
    # Dotted names are admitted through their whitelisted root; plain
    # __import__ semantics: the ROOT module is what the gate returns, and
    # the interpreter binds submodule `a.b` afterwards (see the subprocess
    # matplotlib test below for the end-user `import a.b as c` behaviour).
    assert sandbox_import("collections.abc") is collections


def test_sandbox_import_denies_forbidden_modules():
    for name in ("os", "subprocess", "socket", "sys", "importlib"):
        with pytest.raises(SandboxImportError) as exc:
            sandbox_import(name)
        assert "not allowed in the sandbox" in str(exc.value)


def test_sandbox_import_denies_relative_imports():
    with pytest.raises(SandboxImportError):
        sandbox_import("anything", level=1)


# --- harness 1: the sidecar bootstrap, as a real subprocess --------------

_STUB_PANDAS = "class _FakeDF:\n    def __len__(self):\n        return 3\n\n\ndef read_csv(path_or_buffer):\n    return _FakeDF()\n"

_STUB_MATPLOTLIB_INIT = "def use(*args, **kwargs):\n    pass\n"


def _run_bootstrap(code: str, tmp_path) -> subprocess.CompletedProcess:
    """Run the real bootstrap with stubbed heavy deps and a staged CSV."""
    stub_root = tmp_path / "stubs"
    (stub_root / "matplotlib").mkdir(parents=True)
    (stub_root / "pandas.py").write_text(_STUB_PANDAS)
    (stub_root / "numpy.py").write_text("")
    (stub_root / "matplotlib" / "__init__.py").write_text(_STUB_MATPLOTLIB_INIT)
    (stub_root / "matplotlib" / "pyplot.py").write_text("")

    input_dir = tmp_path / "input"
    input_dir.mkdir()
    (input_dir / "ventas.csv").write_text("region,ventas\nnorte,1200\nsur,800\n")
    (tmp_path / "output").mkdir()

    env = {
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "HOME": str(tmp_path),
        "PYTHONPATH": str(stub_root),
        "PYTHONDONTWRITEBYTECODE": "1",
        "MCP_SANDBOX_CODE": base64.b64encode(code.encode()).decode(),
        "INPUT_DIR": str(input_dir),
        "OUTPUT_DIR": str(tmp_path / "output"),
    }
    return subprocess.run(
        [sys.executable, _BOOTSTRAP],
        env=env,
        capture_output=True,
        text=True,
        timeout=120,
        cwd=str(tmp_path),
    )


def test_bootstrap_runs_criterion_payload_with_imports(tmp_path):
    """
    Acceptance criterion of t_52311b49: `import pandas as pd` + df binding
    must exit 0 through the bootstrap, never "__import__ not found".
    """
    result = _run_bootstrap("import pandas as pd\n\nprint(len(df))\n", tmp_path)
    assert result.returncode == 0, result.stdout + result.stderr
    assert '__RESULT__:{"success": true' in result.stdout
    # len(df) printed on stdout as a bare line between the protocol chunks.
    assert "\n3\n" in result.stdout


def test_bootstrap_also_tolerates_the_matplotlib_import(tmp_path):
    """The prompt (rule 10) orders `import matplotlib.pyplot as plt`."""
    result = _run_bootstrap(
        "import matplotlib.pyplot as plt\nprint(plt is not None)\n", tmp_path
    )
    assert result.returncode == 0, result.stdout + result.stderr
    assert "__RESULT__" in result.stdout


def test_bootstrap_denies_import_outside_whitelist(tmp_path):
    result = _run_bootstrap("import os\nprint(os.getcwd())\n", tmp_path)
    assert result.returncode == 1
    assert "not allowed in the sandbox" in result.stdout
    assert "/usr" not in result.stdout  # nothing of the payload ran


def test_bootstrap_denies_dotted_forbidden_module(tmp_path):
    result = _run_bootstrap("import subprocess.check_output\n", tmp_path)
    assert result.returncode == 1
    assert "not allowed in the sandbox" in result.stdout


def test_bootstrap_still_denies_eval(tmp_path):
    """The gate must not re-open the other blocked builtins."""
    result = _run_bootstrap("value = eval('1 + 1')\nprint(value)\n", tmp_path)
    assert result.returncode == 1
    assert "eval" in result.stdout  # NameError surfaces verbatim


# --- harness 2: the in-process fallback (execute_code_safely) -------------


def _load_main():
    """Load tools/data_analysis/main.py by path (importlib, like test_main.py)."""
    main_path = os.path.join(_HERE, "main.py")
    spec = importlib.util.spec_from_file_location(
        "data_analysis_main_import_gate", main_path
    )
    if spec is None or spec.loader is None:  # pragma: no cover
        raise RuntimeError(f"cannot import {main_path}: no module spec")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.fixture
def main(monkeypatch):
    monkeypatch.delenv("SANDBOX_REQUIRED", raising=False)
    return _load_main()


def test_fallback_harness_runs_whitelisted_import(main):
    result = main.execute_code_safely(
        "import json\nprint(json.dumps([1, 2]))\nresult='ok'",
        df=None,
        request_id="t-import-gate",
    )
    assert result["success"] is True, result["stderr"]
    assert "[1, 2]" in result["stdout"]


def test_fallback_harness_runs_the_criterion_pandas_payload(main):
    pytest.importorskip("pandas")  # local/container env; absent in slim CI
    result = main.execute_code_safely(
        "import pandas as pd\nresult=len(pd.DataFrame({'a': [1, 2, 3]}))",
        df=None,
        request_id="t-import-gate",
    )
    assert result["success"] is True, result["stderr"]
    assert result["result"] == 3


def test_fallback_harness_denies_os(main):
    result = main.execute_code_safely(
        "import os\nprint(os.getcwd())", df=None, request_id="t-import-gate"
    )
    assert result["success"] is False
    assert "not allowed in the sandbox" in result["stderr"]