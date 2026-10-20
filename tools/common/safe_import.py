#!/usr/bin/env python3
"""
Shared import gate for sandboxed code execution (contract:
docs/SANDBOX_SIDECAR_API.md §7.1).

LLM-generated analysis payloads legitimately start with
`import pandas as pd`, but handing user code the real `__import__`
builtin would also grant imports of `os`, `subprocess`, `socket`, …
Both execution harnesses therefore bind `__import__` to
`sandbox_import`, which only admits modules whose ROOT module is in
`SANDBOX_ALLOWED_MODULES` and refuses relative imports:

- tools/data_analysis/sandbox_bootstrap.py (sidecar container harness)
- tools/data_analysis/main.py :: execute_code_safely (in-process fallback)

This module must stay dependency-free (no top-level imports of pandas or
friends): the CI python job has none of them and the bootstrap runs the
import before anything else is available.

The whitelist is the honest, enforced version of the prompt rules in
tools/data_analysis/main.py (rule 15: "DO NOT import any modules except
matplotlib for visualizations"). Keep both in sync.
"""

SANDBOX_ALLOWED_MODULES = frozenset(
    {
        "pandas",
        "numpy",
        "matplotlib",
        "json",
        "datetime",
        "math",
        "re",
        "collections",
    }
)


class SandboxImportError(ImportError):
    """Raised when user code imports a module outside the whitelist."""


def sandbox_import(name, globals=None, locals=None, fromlist=(), level=0):
    """
    `__import__` replacement that only admits whitelisted root modules.

    Dotted names (`import matplotlib.pyplot as plt`) are checked by their
    ROOT component: the root still has to be whitelisted, so no dotted
    path can smuggle a forbidden module, and the real `__import__` returns
    the same objects the harness pre-binds (pd, np, plt, json, …).
    """
    root = name.split(".")[0] if name else ""
    if level != 0 or root not in SANDBOX_ALLOWED_MODULES:
        raise SandboxImportError(
            f"Import of {name!r} is not allowed in the sandbox. "
            f"Allowed modules: {', '.join(sorted(SANDBOX_ALLOWED_MODULES))}"
        )
    return __import__(name, globals, locals, fromlist, level)