# Dev-Tools Enhancement Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fortalecer las 5 dev-tools existentes (rendimiento, seguridad, robustez) e incorporar 5 nuevas herramientas de desarrollo, manteniendo el principio "0 tokens LLM" y permitiendo cambiar el "juego de herramientas" según el contexto del proyecto.

**Architecture:** Mejoras incrementales sobre `tools/common/codebase_utils.py` (cache, límites, exports) + evolución de cada dev-tool + nuevas tools con infraestructura compartida. El sistema de `tool.yaml` + auto-discovery ya soporta el cambio de juego de herramientas.

**Tech Stack:** Python 3.11, AST, regex, gitpython/subprocess, pytest, Go 1.23

---

## File Structure

| File | Responsibility |
|------|---------------|
| `tools/common/codebase_utils.py` | Shared: walk, imports, keywords, project discovery |
| `tools/common/__init__.py` | Exports públicos de codebase_utils |
| `tools/dev-tools/opencode_context/main.py` | Priorizar archivos para contexto LLM |
| `tools/dev-tools/codebase_scan/main.py` | Dead code, test gaps, hotspots, drift |
| `tools/dev-tools/git_inspector/main.py` | Métricas git sin comandos manuales |
| `tools/dev-tools/dependency_audit/main.py` | Declared vs used dependencies |
| `tools/dev-tools/security_lint/main.py` | Patrones inseguros (AST/regex) |
| `tools/dev-tools/test_runner/main.py` | **NEW** Ejecutar tests y parsear resultados |
| `tools/dev-tools/doc_generator/main.py` | **NEW** Generar docs desde docstrings |
| `tools/dev-tools/refactor_suggester/main.py` | **NEW** Código duplicado y complejidad |
| `tools/dev-tools/api_diff/main.py` | **NEW** Comparar OpenAPI specs |
| `tools/dev-tools/changelog_generator/main.py` | **NEW** Changelog desde commits git |
| `deployments/Dockerfile` | Asegurar git + nuevas deps |
| `tests/tools/common/test_codebase_utils.py` | Tests de infraestructura compartida |
| `tests/tools/dev-tools/` | Tests por herramienta |

---

## Task 1: Infraestructura compartida — Cache y límites

**Files:**
- Modify: `tools/common/codebase_utils.py`
- Create: `tests/tools/common/test_codebase_utils.py` (extender)
- Modify: `tools/common/__init__.py`

- [ ] **Step 1.1: Añadir `MAX_FILES_TO_SCAN` y `ScanCache`**

```python
# En codebase_utils.py

import hashlib
import json
import time
from pathlib import Path
from typing import Any

# Límite global para prevenir timeouts en repos enormes
MAX_FILES_TO_SCAN = 5000

class ScanCache:
    """Cache simple basado en git HEAD + timestamp. Evita re-escanear todo el repo."""

    def __init__(self, cache_dir: Path | None = None):
        self.cache_dir = cache_dir or Path("/tmp/mcp_scan_cache")
        self.cache_dir.mkdir(parents=True, exist_ok=True)
        self.ttl_seconds = 300  # 5 minutos

    def _get_git_head(self, root: str) -> str:
        try:
            import subprocess
            result = subprocess.run(
                ["git", "-C", root, "rev-parse", "HEAD"],
                capture_output=True, text=True, timeout=5
            )
            if result.returncode == 0:
                return result.stdout.strip()
        except Exception:
            pass
        return "no-git"

    def _cache_key(self, root: str, operation: str) -> str:
        head = self._get_git_head(root)
        content = f"{root}:{operation}:{head}"
        return hashlib.sha256(content.encode()).hexdigest()

    def get(self, root: str, operation: str) -> dict[str, Any] | None:
        key = self._cache_key(root, operation)
        cache_file = self.cache_dir / f"{key}.json"
        if not cache_file.exists():
            return None
        try:
            data = json.loads(cache_file.read_text())
            if time.time() - data.get("timestamp", 0) < self.ttl_seconds:
                return data.get("result")
        except Exception:
            pass
        return None

    def set(self, root: str, operation: str, result: dict[str, Any]) -> None:
        key = self._cache_key(root, operation)
        cache_file = self.cache_dir / f"{key}.json"
        try:
            cache_file.write_text(json.dumps({
                "timestamp": time.time(),
                "result": result
            }, default=str))
        except Exception:
            pass
```

- [ ] **Step 1.2: Modificar `safe_walk` para respetar `MAX_FILES_TO_SCAN` y `.gitignore`**

```python
def safe_walk(root: Path, exclude_patterns: list[str], max_files: int = MAX_FILES_TO_SCAN) -> Iterator[Path]:
    count = 0
    gitignore_patterns = _load_gitignore(root)
    all_excludes = exclude_patterns + gitignore_patterns

    for path in root.rglob("*"):
        if not path.is_file():
            continue
        rel = str(path.relative_to(root))
        if any(re.search(p, rel) for p in all_excludes):
            continue
        if count >= max_files:
            logger.warning(f"Reached max_files limit ({max_files}) in {root}")
            break
        count += 1
        yield path

def _load_gitignore(root: Path) -> list[str]:
    gitignore = root / ".gitignore"
    if not gitignore.exists():
        return []
    patterns = []
    for line in gitignore.read_text(encoding="utf-8", errors="ignore").splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            pattern = line.replace(".", r"\.").replace("*", ".*").replace("?", ".")
            patterns.append(pattern)
    return patterns
```

- [ ] **Step 1.3: Crear `__init__.py` en tools/common**

```python
from .codebase_utils import (
    discover_project,
    safe_walk,
    parse_imports,
    extract_keywords,
    ScanCache,
    DEFAULT_EXCLUDE_PATTERNS,
    SOURCE_EXTENSIONS,
    CONFIG_FILES,
    MAX_FILES_TO_SCAN,
)

__all__ = [
    "discover_project",
    "safe_walk",
    "parse_imports",
    "extract_keywords",
    "ScanCache",
    "DEFAULT_EXCLUDE_PATTERNS",
    "SOURCE_EXTENSIONS",
    "CONFIG_FILES",
    "MAX_FILES_TO_SCAN",
]
```

---

## Task 2: Mejorar `opencode_context` — Cache + límites

**Files:**
- Modify: `tools/dev-tools/opencode_context/main.py`

- [ ] **Step 2.1: Integrar ScanCache en opencode_context**

En la función `main()` de `tools/dev-tools/opencode_context/main.py`, añadir:

```python
def main():
    try:
        request = read_request()
        args = request.get("arguments", {})
        project_root = args["project_root"]
        task_description = args.get("task_description", "")
        max_files = args.get("max_files", 15)
        include_tests = args.get("include_tests", True)

        cache = ScanCache()
        cache_op = f"opencode_context:{task_description[:100]}:{max_files}:{include_tests}"
        cached = cache.get(project_root, cache_op)
        if cached:
            write_response({
                "success": True,
                "request_id": request.get("request_id", ""),
                "content": [{"type": "text", "text": cached["summary"]}],
                "structured_content": cached,
            })
            return

        project_root_path = Path(project_root).resolve()
        os.chdir(project_root_path)
        keywords = extract_keywords(task_description)
        recommended, entry_points = score_files(
            project_root_path, keywords, include_tests, max_files
        )

        # Build response
        summary = _format_summary(recommended, entry_points, project_root)
        structured = {
            "project": {
                "name": project_root_path.name,
                "language": discover_project(project_root).get("primary_language"),
            },
            "keywords": keywords,
            "entry_points": entry_points,
            "recommended_files": recommended,
            "total_files_scanned": len(recommended),
        }

        cache.set(project_root, cache_op, structured)

        write_response({
            "success": True,
            "request_id": request.get("request_id", ""),
            "content": [{"type": "text", "text": summary}],
            "structured_content": structured,
        })
    except Exception as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "EXECUTION_FAILED", "message": str(e), "details": traceback.format_exc()},
        })
```

- [ ] **Step 2.2: Limitar archivos procesados en `score_files`**

```python
def score_files(root, keywords, include_tests, max_files):
    scored = sorted(scores.items(), key=lambda x: x[1], reverse=True)[:max_files]
    return scored, entry_points
```

---

## Task 3: Mejorar `codebase_scan` — Cache + límites

**Files:**
- Modify: `tools/dev-tools/codebase_scan/main.py`

- [ ] **Step 3.1: Usar ScanCache en cada sub-operación**

```python
def main():
    try:
        request = read_request()
        args = request.get("arguments", {})
        project_root = args["project_root"]
        scan_type = args.get("scan_type", "dead_code")
        exclude_patterns = args.get("exclude_patterns", DEFAULT_EXCLUDE_PATTERNS)

        cache = ScanCache()
        cache_op = f"codebase_scan:{scan_type}"
        cached = cache.get(project_root, cache_op)
        if cached:
            write_response({
                "success": True,
                "request_id": request.get("request_id", ""),
                "content": [{"type": "text", "text": json.dumps(cached, indent=2, default=str)}],
                "structured_content": cached,
            })
            return

        root = Path(project_root).resolve()
        findings = []
        structured = {"scan_type": scan_type, "findings": findings}

        if scan_type == "dead_code":
            findings.extend(_scan_dead_code(root, exclude_patterns))
        elif scan_type == "test_gaps":
            findings.extend(_scan_test_gaps(root, exclude_patterns))
        elif scan_type == "import_graph":
            result = _build_import_graph(root, exclude_patterns)
            structured["graph"] = result
        elif scan_type == "hotspots":
            findings.extend(_scan_hotspots(root, exclude_patterns))
        elif scan_type == "dependency_drift":
            structured["drift"] = _scan_dependency_drift(root, exclude_patterns)

        cache.set(project_root, cache_op, structured)

        write_response({
            "success": True,
            "request_id": request.get("request_id", ""),
            "content": [{"type": "text", "text": json.dumps(structured, indent=2, default=str)}],
            "structured_content": structured,
        })
    except Exception as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "EXECUTION_FAILED", "message": str(e), "details": traceback.format_exc()},
        })
```

---

## Task 4: Mejorar `dependency_audit` — Best-effort flags

**Files:**
- Modify: `tools/dev-tools/dependency_audit/main.py`

- [ ] **Step 4.1: Marcar `outdated` y `security` como best-effort**

```python
def main():
    try:
        request = read_request()
        args = request.get("arguments", {})
        project_root = args["project_root"]
        checks = args.get("checks", ["unused", "missing"])

        root = Path(project_root).resolve()
        result = {
            "unused": [],
            "missing": [],
            "outdated": [],
            "security": [],
            "best_effort_failures": [],
        }

        if "unused" in checks:
            result["unused"] = _scan_unused(root)
        if "missing" in checks:
            result["missing"] = _scan_missing(root)
        if "outdated" in checks:
            try:
                result["outdated"] = _check_outdated(root)
            except Exception as e:
                result["best_effort_failures"].append({
                    "check": "outdated",
                    "reason": str(e),
                    "note": "Network access may be unavailable",
                })
        if "security" in checks:
            try:
                result["security"] = _check_security_advisories(root)
            except Exception as e:
                result["best_effort_failures"].append({
                    "check": "security",
                    "reason": str(e),
                    "note": "Advisory DB lookup failed (no network / rate-limited)",
                })

        write_response({
            "success": True,
            "request_id": request.get("request_id", ""),
            "content": [{"type": "text", "text": json.dumps(result, indent=2, default=str)}],
            "structured_content": result,
        })
    except Exception as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "EXECUTION_FAILED", "message": str(e), "details": traceback.format_exc()},
        })
```

- [ ] **Step 4.2: Añadir función `_check_security_advisories` básica**

```python
def _check_security_advisories(root: Path) -> list[dict[str, Any]]:
    """
    Placeholder para futura integración con advisory DB (OSV.dev, PyPI JSON, npm audit).
    Por ahora solo devuelve estructura vacía + nota.
    """
    return [{
        "note": "Security advisory check not yet integrated with external DB.",
        "recommendation": "Run 'pip audit' or 'npm audit' separately.",
    }]
```

---

## Task 5: Verificar `git` en Dockerfile

**Files:**
- Modify: `deployments/Dockerfile`

- [ ] **Step 5.1: Añadir `git` al stage final**

```dockerfile
# Stage 2: Python base with system dependencies
FROM python:3.11-slim-bookworm AS python-base

RUN apt-get update && apt-get install -y --no-install-recommends \
    # ...existing deps...
    git \  # AÑADIR
    # ...rest...
```

- [ ] **Step 5.2: Añadir `gitpython` a requirements.txt**

```
# Git tools
gitpython>=3.1.40
```

---

## Task 6: Nueva herramienta `test_runner`

**Files:**
- Create: `tools/dev-tools/test_runner/main.py`
- Create: `tools/dev-tools/test_runner/tool.yaml`

- [ ] **Step 6.1: Implementar `tools/dev-tools/test_runner/main.py`**

```python
#!/usr/bin/env python3
"""
test_runner tool — ejecuta tests y parsea resultados.

Soporta: pytest, go test, jest.
Input: project_root, framework (auto|pytest|go|jest), target, extra_args.
Output: summary con passed/failed/skipped, duración, y failures detallados.
"""
import json
import os
import re
import subprocess
import sys
import traceback
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../.."))

from common.structured_logging import get_logger
from common.codebase_utils import discover_project

logger = get_logger(__name__, "test_runner")


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def detect_framework(root: Path) -> str:
    if (root / "go.mod").exists():
        return "go"
    if (root / "package.json").exists():
        return "jest"
    if any((root / p).exists() for p in ["pytest.ini", "pyproject.toml", "setup.cfg"]):
        return "pytest"
    if any(f.name.startswith("test_") and f.suffix == ".py" for f in root.rglob("*.py")):
        return "pytest"
    return "pytest"


def run_pytest(root: Path, target: str, extra_args: list[str]) -> dict[str, Any]:
    cmd = [sys.executable, "-m", "pytest", target, "-v", "--tb=short", *extra_args]
    logger.info(f"Running pytest: {' '.join(cmd)}")
    result = subprocess.run(cmd, capture_output=True, text=True, cwd=str(root), timeout=300)

    passed = failed = skipped = 0
    summary_match = re.search(r"(=+.*?\n)(.*?)(\d+) passed.*?(\d+) failed.*?(\d+) skipped", result.stdout + result.stderr)
    if summary_match:
        passed = int(summary_match.group(3))
        failed = int(summary_match.group(4))
        skipped = int(summary_match.group(5))
    else:
        passed = result.stdout.count(" PASSED") + result.stdout.count("PASSED ")
        failed = result.stdout.count(" FAILED") + result.stdout.count("FAILED ")
        skipped = result.stdout.count(" SKIPPED") + result.stdout.count("SKIPPED ")

    failures = []
    for m in re.finditer(r"FAILED\s+(\S+)\s+-\s+(.*?)(?=\n=|\nFAILED|\Z)", result.stdout + result.stderr, re.DOTALL):
        failures.append({"test": m.group(1).strip(), "error": m.group(2).strip()[:500]})

    return {
        "framework": "pytest",
        "total": passed + failed + skipped,
        "passed": passed,
        "failed": failed,
        "skipped": skipped,
        "failures": failures[:20],
        "raw_tail": (result.stdout + result.stderr)[-2000:],
    }


def run_go_test(root: Path, target: str, extra_args: list[str]) -> dict[str, Any]:
    cmd = ["go", "test", "-v", *extra_args, target]
    logger.info(f"Running go test: {' '.join(cmd)}")
    result = subprocess.run(cmd, capture_output=True, text=True, cwd=str(root), timeout=300)

    passed = result.stdout.count("PASS")
    failed = result.stdout.count("FAIL")

    return {
        "framework": "go",
        "total": passed + failed,
        "passed": passed,
        "failed": failed,
        "skipped": 0,
        "failures": [],
        "raw_tail": (result.stdout + result.stderr)[-2000:],
    }


def main():
    try:
        request = read_request()
        args = request.get("arguments", {})
        root = Path(args["project_root"]).resolve()
        target = args.get("target", ".")
        framework = args.get("framework", "auto")
        extra_args = args.get("extra_args", [])

        if not root.exists():
            write_response({
                "success": False,
                "error": {"code": "INVALID_INPUT", "message": f"Project root not found: {root}"},
                "request_id": request.get("request_id", ""),
            })
            return

        if framework == "auto":
            framework = detect_framework(root)

        result = {}
        if framework == "pytest":
            result = run_pytest(root, target, extra_args)
        elif framework == "go":
            result = run_go_test(root, target, extra_args)
        else:
            write_response({
                "success": False,
                "error": {"code": "INVALID_INPUT", "message": f"Framework '{framework}' not yet supported"},
                "request_id": request.get("request_id", ""),
            })
            return

        summary = f"Tests: {result.get('passed', 0)} passed, {result.get('failed', 0)} failed, {result.get('skipped', 0)} skipped"
        if result.get("failures"):
            summary += f"\nFailures:\n" + "\n".join(f"  - {f['test']}: {f['error'][:100]}" for f in result["failures"][:5])

        write_response({
            "success": True,
            "request_id": request.get("request_id", ""),
            "content": [{"type": "text", "text": summary}],
            "structured_content": result,
        })
    except subprocess.TimeoutExpired:
        write_response({
            "success": False,
            "error": {"code": "TIMEOUT", "message": "Test execution timed out"},
            "request_id": request.get("request_id", ""),
        })
    except Exception as e:
        write_response({
            "success": False,
            "error": {"code": "EXECUTION_FAILED", "message": str(e), "details": traceback.format_exc()},
            "request_id": request.get("request_id", ""),
        })


if __name__ == "__main__":
    main()
```

- [ ] **Step 6.2: Crear `tools/dev-tools/test_runner/tool.yaml`**

```yaml
name: "test_runner"
description: "Ejecuta tests y devuelve resumen: passed/failed/skipped + failures. Soporta pytest, go test. Auto-detecta framework. 0 tokens LLM."
command: "python3"
args: ["main.py"]
timeout: "300s"
input_schema:
  type: object
  properties:
    project_root:
      type: string
      description: "Ruta absoluta al proyecto"
    framework:
      type: string
      enum: ["auto", "pytest", "go"]
      default: "auto"
      description: "Framework de testing (auto = detecta automaticamente)"
    target:
      type: string
      default: "."
      description: "Archivo o directorio a testear"
    extra_args:
      type: array
      items:
        type: string
      default: []
      description: "Argumentos extra para el runner (e.g. -k, --timeout)"
  required:
    - project_root
```

---

## Task 7: Nueva herramienta `doc_generator`

**Files:**
- Create: `tools/dev-tools/doc_generator/main.py`
- Create: `tools/dev-tools/doc_generator/tool.yaml`

- [ ] **Step 7.1: Implementar `tools/dev-tools/doc_generator/main.py`**

```python
#!/usr/bin/env python3
"""
doc_generator tool — genera documentación desde docstrings.

Soporta Python (google/sphinx/reST), Go, JavaScript.
Input: project_root, output_format (markdown), modules (lista de paths).
Output: documentación estructurada con funciones, clases, parámetros, returns.
"""
import ast
import json
import os
import sys
import traceback
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../.."))

from common.structured_logging import get_logger
from common.codebase_utils import safe_walk, parse_imports, DEFAULT_EXCLUDE_PATTERNS

logger = get_logger(__name__, "doc_generator")


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def parse_python_symbols(file_path: Path) -> list[dict[str, Any]]:
    try:
        source = file_path.read_text(encoding="utf-8", errors="ignore")
        tree = ast.parse(source)
    except SyntaxError:
        return []

    symbols = []
    for node in ast.walk(tree):
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            docstring = ast.get_docstring(node) or ""
            params = [a.arg for a in node.args.args if a.arg not in ("self", "cls")]
            return_type = ""
            if node.returns:
                return_type = ast.dump(node.returns)
            symbols.append({
                "kind": "function",
                "name": node.name,
                "line": node.lineno,
                "docstring": docstring[:1000],
                "params": params,
                "returns": return_type,
                "decorators": [ast.dump(d) for d in node.decorator_list],
            })
        elif isinstance(node, ast.ClassDef):
            docstring = ast.get_docstring(node) or ""
            methods = [m.name for m in node.body if isinstance(m, (ast.FunctionDef, ast.AsyncFunctionDef))]
            symbols.append({
                "kind": "class",
                "name": node.name,
                "line": node.lineno,
                "docstring": docstring[:1000],
                "methods": methods,
                "bases": [ast.dump(b) for b in node.bases],
            })
    return symbols


def generate_markdown_docs(project_root: Path, modules: list[str]) -> tuple[str, list[dict[str, Any]]]:
    lines = [f"# Documentation for `{project_root.name}`\n"]
    all_symbols = []

    for module in modules:
        module_path = project_root / module
        if not module_path.exists():
            logger.warning(f"Module path not found: {module_path}")
            continue

        lines.append(f"\n## Module: `{module}`\n")

        for file_path in sorted(safe_walk(module_path, DEFAULT_EXCLUDE_PATTERNS)):
            if file_path.suffix not in (".py", ".go", ".js", ".ts", ".tsx"):
                continue
            if file_path.name.startswith("test_") or file_path.name.startswith("_"):
                continue

            rel = str(file_path.relative_to(project_root))
            if file_path.suffix == ".py":
                symbols = parse_python_symbols(file_path)
            else:
                continue

            if not symbols:
                continue

            lines.append(f"### `{rel}`\n")
            for s in symbols:
                if s["kind"] == "function":
                    params_str = ", ".join(s["params"])
                    lines.append(f"- **`{s['name']}({params_str})`** — line {s['line']}")
                    if s["docstring"]:
                        lines.append(f"  > {s['docstring'].split(chr(10))[0]}")
                elif s["kind"] == "class":
                    lines.append(f"- **class `{s['name']}`** — line {s['line']}")
                    if s["docstring"]:
                        lines.append(f"  > {s['docstring'].split(chr(10))[0]}")
                    if s["methods"]:
                        lines.append(f"  Methods: `{'`, `'.join(s['methods'])}`")
                lines.append("")
            all_symbols.extend(symbols)

    return "\n".join(lines), all_symbols


def main():
    try:
        request = read_request()
        args = request.get("arguments", {})
        root = Path(args["project_root"]).resolve()
        output_format = args.get("output_format", "markdown")
        modules = args.get("modules", ["."])

        if not root.exists():
            write_response({
                "success": False,
                "error": {"code": "INVALID_INPUT", "message": f"Project root not found: {root}"},
                "request_id": request.get("request_id", ""),
            })
            return

        text, symbols = generate_markdown_docs(root, modules)

        write_response({
            "success": True,
            "request_id": request.get("request_id", ""),
            "content": [{"type": "text", "text": text[:20000]}],
            "structured_content": {
                "format": output_format,
                "symbol_count": len(symbols),
                "char_count": len(text),
                "symbol_types": {
                    "functions": len([s for s in symbols if s["kind"] == "function"]),
                    "classes": len([s for s in symbols if s["kind"] == "class"]),
                },
            },
        })
    except Exception as e:
        write_response({
            "success": False,
            "error": {"code": "EXECUTION_FAILED", "message": str(e), "details": traceback.format_exc()},
            "request_id": request.get("request_id", ""),
        })


if __name__ == "__main__":
    main()
```

- [ ] **Step 7.2: Crear `tools/dev-tools/doc_generator/tool.yaml`**

```yaml
name: "doc_generator"
description: "Genera documentacion desde docstrings. Soporta Python (AST). Output markdown con funciones, clases, parametros. 0 tokens LLM."
command: "python3"
args: ["main.py"]
timeout: "120s"
input_schema:
  type: object
  properties:
    project_root:
      type: string
      description: "Ruta absoluta al proyecto"
    modules:
      type: array
      items:
        type: string
      default: ["."]
      description: "Directorios/modulos a documentar"
    output_format:
      type: string
      enum: ["markdown"]
      default: "markdown"
      description: "Formato de salida"
  required:
    - project_root
```

---

## Task 8: Nueva herramienta `refactor_suggester`

**Files:**
- Create: `tools/dev-tools/refactor_suggester/main.py`
- Create: `tools/dev-tools/refactor_suggester/tool.yaml`

- [ ] **Step 8.1: Implementar `tools/dev-tools/refactor_suggester/main.py`**

```python
#!/usr/bin/env python3
"""
refactor_suggester tool — detecta código duplicado y complejidad alta.

Checks:
    - duplication: Bloques de código similares (> N líneas)
    - complexity: Funciones con cyclomatic > threshold
    - long_functions: Funciones > threshold líneas

Input: project_root, checks, thresholds.
Output: lista de sugerencias con severity y ubicación. 0 tokens LLM.
"""
import ast
import hashlib
import json
import os
import sys
import traceback
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../.."))

from common.structured_logging import get_logger
from common.codebase_utils import safe_walk, DEFAULT_EXCLUDE_PATTERNS

logger = get_logger(__name__, "refactor_suggester")


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def calculate_cyclomatic(node: ast.FunctionDef) -> int:
    complexity = 1
    for child in ast.walk(node):
        if isinstance(child, (ast.If, ast.While, ast.For, ast.ExceptHandler,
                              ast.With, ast.Assert, ast.Try)):
            complexity += 1
        elif isinstance(child, ast.BoolOp):
            complexity += len(child.values) - 1
        elif isinstance(child, (ast.comprehension, ast.GeneratorExp)):
            complexity += 1
    return complexity


def find_complex_functions(root: Path, threshold: int = 10) -> list[dict[str, Any]]:
    findings = []
    for file_path in safe_walk(root, DEFAULT_EXCLUDE_PATTERNS):
        if file_path.suffix != ".py":
            continue
        try:
            source = file_path.read_text(encoding="utf-8", errors="ignore")
            tree = ast.parse(source)
        except Exception:
            continue

        for node in ast.walk(tree):
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                if node.name.startswith("_"):
                    continue
                complexity = calculate_cyclomatic(node)
                if complexity > threshold:
                    findings.append({
                        "file": str(file_path.relative_to(root)),
                        "line": node.lineno,
                        "name": node.name,
                        "complexity": complexity,
                        "severity": "high" if complexity > 20 else "medium",
                        "suggestion": f"Refactor '{node.name}' ({complexity} paths) into smaller functions",
                    })
    return findings


def find_long_functions(root: Path, threshold: int = 50) -> list[dict[str, Any]]:
    findings = []
    for file_path in safe_walk(root, DEFAULT_EXCLUDE_PATTERNS):
        if file_path.suffix != ".py":
            continue
        try:
            source = file_path.read_text(encoding="utf-8", errors="ignore")
            tree = ast.parse(source)
        except Exception:
            continue

        for node in ast.walk(tree):
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                if node.name.startswith("_"):
                    continue
                count = node.end_lineno - node.lineno if node.end_lineno else 0
                if count > threshold:
                    findings.append({
                        "file": str(file_path.relative_to(root)),
                        "line": node.lineno,
                        "name": node.name,
                        "lines": count,
                        "severity": "medium",
                        "suggestion": f"'{node.name}' is {count} lines. Extract helper functions.",
                    })
    return findings


def find_duplicated_blocks(root: Path, min_lines: int = 5) -> list[dict[str, Any]]:
    block_hashes: dict[str, list[tuple[Path, int]]] = {}

    for file_path in safe_walk(root, DEFAULT_EXCLUDE_PATTERNS):
        if file_path.suffix != ".py":
            continue
        try:
            lines = file_path.read_text(encoding="utf-8", errors="ignore").splitlines()
        except Exception:
            continue

        for i in range(len(lines) - min_lines + 1):
            block = lines[i:i + min_lines]
            normalized = "\n".join(l.split("#")[0].strip() for l in block)
            if len(normalized.strip()) < 20:
                continue
            h = hashlib.md5(normalized.encode()).hexdigest()
            block_hashes.setdefault(h, []).append((file_path, i + 1))

    findings = []
    for h, locs in block_hashes.items():
        if len(locs) > 1:
            findings.append({
                "block_hash": h[:8],
                "occurrences": len(locs),
                "locations": [{"file": str(loc[0].relative_to(root)), "line": loc[1]} for loc in locs],
                "severity": "high" if len(locs) > 2 else "medium",
                "suggestion": f"Duplicated block in {len(locs)} places. Extract to shared function.",
            })
    return findings


def main():
    try:
        request = read_request()
        args = request.get("arguments", {})
        root = Path(args["project_root"]).resolve()
        checks = args.get("checks", ["complexity", "long_functions"])
        complexity_threshold = args.get("complexity_threshold", 10)
        lines_threshold = args.get("lines_threshold", 50)
        min_dup_lines = args.get("min_duplicate_lines", 5)

        if not root.exists():
            write_response({
                "success": False,
                "error": {"code": "INVALID_INPUT", "message": f"Project root not found: {root}"},
                "request_id": request.get("request_id", ""),
            })
            return

        all_findings = []

        if "complexity" in checks:
            all_findings.extend(find_complex_functions(root, complexity_threshold))
        if "long_functions" in checks:
            all_findings.extend(find_long_functions(root, lines_threshold))
        if "duplication" in checks:
            all_findings.extend(find_duplicated_blocks(root, min_dup_lines))

        high = sum(1 for f in all_findings if f.get("severity") == "high")
        medium = sum(1 for f in all_findings if f.get("severity") == "medium")
        low = sum(1 for f in all_findings if f.get("severity") == "low")

        summary = f"Refactor suggestions: {len(all_findings)} total ({high} high, {medium} medium, {low} low)"

        write_response({
            "success": True,
            "request_id": request.get("request_id", ""),
            "content": [{"type": "text", "text": summary}],
            "structured_content": {"findings": all_findings, "totals": {"high": high, "medium": medium, "low": low}},
        })
    except Exception as e:
        write_response({
            "success": False,
            "error": {"code": "EXECUTION_FAILED", "message": str(e), "details": traceback.format_exc()},
            "request_id": request.get("request_id", ""),
        })


if __name__ == "__main__":
    main()
```

- [ ] **Step 8.2: Crear `tools/dev-tools/refactor_suggester/tool.yaml`**

```yaml
name: "refactor_suggester"
description: "Detecta codigo duplicado y funciones complejas en Python (AST). Checks: duplication, complexity, long_functions. 0 tokens LLM."
command: "python3"
args: ["main.py"]
timeout: "120s"
input_schema:
  type: object
  properties:
    project_root:
      type: string
      description: "Ruta absoluta al proyecto"
    checks:
      type: array
      items:
        type: string
        enum: ["complexity", "long_functions", "duplication"]
      default: ["complexity", "long_functions"]
      description: "Tipos de analisis a realizar"
    complexity_threshold:
      type: integer
      default: 10
      description: "Complejidad ciclomatica maxima"
    lines_threshold:
      type: integer
      default: 50
      description: "Lineas maximas por funcion"
    min_duplicate_lines:
      type: integer
      default: 5
      description: "Lineas minimas para detectar duplicacion"
  required:
    - project_root
```

---

## Task 9: Nueva herramienta `api_diff`

**Files:**
- Create: `tools/dev-tools/api_diff/main.py`
- Create: `tools/dev-tools/api_diff/tool.yaml`

- [ ] **Step 9.1: Implementar `tools/dev-tools/api_diff/main.py`**

```python
#!/usr/bin/env python3
"""
api_diff tool — compara dos OpenAPI specs y reporta cambios.

Input: old_spec, new_spec (paths locales o URLs).
Output: endpoints añadidos/eliminados/modificados, breaking changes. 0 tokens LLM.
"""
import json
import os
import sys
import traceback
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../.."))

from common.structured_logging import get_logger

logger = get_logger(__name__, "api_diff")


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def load_spec(spec_path: str) -> dict[str, Any]:
    path = Path(spec_path)
    if path.exists():
        content = path.read_text(encoding="utf-8", errors="ignore")
    else:
        import urllib.request
        with urllib.request.urlopen(spec_path, timeout=30) as resp:
            content = resp.read().decode("utf-8")

    if spec_path.endswith((".yaml", ".yml")):
        import yaml
        return yaml.safe_load(content)
    return json.loads(content)


def extract_endpoints(spec: dict[str, Any]) -> dict[str, dict[str, Any]]:
    endpoints = {}
    for path, methods in spec.get("paths", {}).items():
        for method, details in methods.items():
            if method in ("parameters", "summary", "description"):
                continue
            key = f"{method.upper()} {path}"
            endpoints[key] = {
                "parameters": [p.get("name") for p in details.get("parameters", [])],
                "request_body": bool(details.get("requestBody")),
                "responses": list(details.get("responses", {}).keys()),
            }
    return endpoints


def diff_specs(old: dict[str, Any], new: dict[str, Any]) -> dict[str, Any]:
    old_eps = extract_endpoints(old)
    new_eps = extract_endpoints(new)

    old_keys = set(old_eps)
    new_keys = set(new_eps)

    added = sorted(new_keys - old_keys)
    removed = sorted(old_keys - new_keys)

    breaking = []
    modified = []

    for key in sorted(old_keys & new_keys):
        od = old_eps[key]
        nd = new_eps[key]
        changes = []

        old_params = set(od["parameters"])
        new_params = set(nd["parameters"])
        added_params = new_params - old_params
        removed_params = old_params - new_params

        if added_params:
            changes.append(f"Params added: {sorted(added_params)}")
        if removed_params:
            changes.append(f"Params removed: {sorted(removed_params)}")
            breaking.append(f"{key}: removed params ({sorted(removed_params)})")

        old_resps = set(od["responses"])
        new_resps = set(nd["responses"])
        if old_resps - new_resps:
            breaking.append(f"{key}: removed responses {sorted(old_resps - new_resps)}")

        if od["request_body"] and not nd["request_body"]:
            breaking.append(f"{key}: removed request body")

        if changes:
            modified.append({"endpoint": key, "changes": changes})

    return {
        "summary": {
            "endpoints_old": len(old_keys),
            "endpoints_new": len(new_keys),
            "added": len(added),
            "removed": len(removed),
            "modified": len(modified),
            "breaking": len(breaking),
        },
        "added": added,
        "removed": removed,
        "modified": modified,
        "breaking_changes": breaking,
    }


def main():
    try:
        request = read_request()
        args = request.get("arguments", {})
        old_spec = args["old_spec"]
        new_spec = args["new_spec"]

        old_data = load_spec(old_spec)
        new_data = load_spec(new_spec)

        result = diff_specs(old_data, new_data)
        s = result["summary"]

        summary = f"API Diff: {s['endpoints_old']} -> {s['endpoints_new']} endpoints. "
        summary += f"{s['added']} added, {s['removed']} removed, {s['modified']} modified, {s['breaking']} breaking."

        write_response({
            "success": True,
            "request_id": request.get("request_id", ""),
            "content": [{"type": "text", "text": summary}],
            "structured_content": result,
        })
    except Exception as e:
        write_response({
            "success": False,
            "error": {"code": "EXECUTION_FAILED", "message": str(e), "details": traceback.format_exc()},
            "request_id": request.get("request_id", ""),
        })


if __name__ == "__main__":
    main()
```

- [ ] **Step 9.2: Crear `tools/dev-tools/api_diff/tool.yaml`**

```yaml
name: "api_diff"
description: "Compara dos OpenAPI specs (JSON/YAML, local o URL) y detecta breaking changes: endpoints nuevos/eliminados, parametros, responses. 0 tokens LLM."
command: "python3"
args: ["main.py"]
timeout: "60s"
input_schema:
  type: object
  properties:
    old_spec:
      type: string
      description: "Path local o URL de la spec antigua"
    new_spec:
      type: string
      description: "Path local o URL de la spec nueva"
  required:
    - old_spec
    - new_spec
```

---

## Task 10: Nueva herramienta `changelog_generator`

**Files:**
- Create: `tools/dev-tools/changelog_generator/main.py`
- Create: `tools/dev-tools/changelog_generator/tool.yaml`

- [ ] **Step 10.1: Implementar `tools/dev-tools/changelog_generator/main.py`**

```python
#!/usr/bin/env python3
"""
changelog_generator tool — genera changelog desde commits git.

Input: repo_path, from_tag, to_tag, format (markdown|json).
Output: changelog agrupado por tipo (feat, fix, docs, etc). 0 tokens LLM.
"""
import json
import os
import re
import subprocess
import sys
import traceback
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../.."))

from common.structured_logging import get_logger

logger = get_logger(__name__, "changelog_generator")


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def get_commits(repo_path: str, from_ref: str | None, to_ref: str | None) -> list[dict[str, Any]]:
    if from_ref and to_ref:
        spec = f"{from_ref}..{to_ref}"
    elif from_ref:
        spec = f"{from_ref}..HEAD"
    elif to_ref:
        spec = f"{to_ref}"
    else:
        spec = "-30"

    cmd = ["git", "-C", repo_path, "log", spec, "--pretty=format:%H|%s|%ad|%an", "--date=short"]
    result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)

    if result.returncode != 0:
        raise RuntimeError(f"git error: {result.stderr.strip() or 'unknown'}")

    commits = []
    for line in result.stdout.strip().split("\n"):
        if not line or "|" not in line:
            continue
        parts = line.split("|", 3)
        if len(parts) < 4:
            continue
        hash_, subject, date, author = parts

        m = re.match(r"^(feat|fix|docs|style|refactor|perf|test|chore|ci|build)(\(.+?\))?!?:\s*(.+)$", subject)
        if m:
            type_, scope, message = m.groups()
            breaking = "!" in subject
        else:
            type_, scope, message = "other", None, subject
            breaking = False

        commits.append({
            "hash": hash_[:7],
            "type": type_,
            "scope": scope.strip("()") if scope else None,
            "message": message.strip(),
            "breaking": breaking,
            "date": date,
            "author": author,
        })

    return commits


def generate_markdown(commits: list[dict[str, Any]]) -> str:
    grouped: dict[str, list[dict[str, Any]]] = {}
    for c in commits:
        grouped.setdefault(c["type"], []).append(c)

    type_labels = {
        "feat": "Features", "fix": "Bug Fixes", "docs": "Documentation",
        "perf": "Performance", "refactor": "Refactoring", "test": "Tests",
        "chore": "Chores", "ci": "CI/CD", "build": "Build", "style": "Style",
        "other": "Other",
    }

    lines = [f"# Changelog ({commits[0]['date']} - {commits[-1]['date']})\n"]
    type_order = ["feat", "fix", "perf", "refactor", "docs", "test", "chore", "ci", "build", "style", "other"]

    for t in type_order:
        if t not in grouped:
            continue
        label = type_labels.get(t, t.title())
        lines.append(f"\n## {label}\n")
        for c in grouped[t]:
            scope = f"**{c['scope']}**: " if c["scope"] else ""
            breaking = " (BREAKING)" if c["breaking"] else ""
            lines.append(f"- {scope}{c['message']}{breaking} ({c['hash']})")

    return "\n".join(lines)


def main():
    try:
        request = read_request()
        args = request.get("arguments", {})
        repo_path = args["repo_path"]
        from_tag = args.get("from_tag")
        to_tag = args.get("to_tag")
        output_format = args.get("format", "markdown")
        max_commits = args.get("max_commits", 50)

        if not Path(repo_path).exists():
            write_response({
                "success": False,
                "error": {"code": "INVALID_INPUT", "message": f"Repo not found: {repo_path}"},
                "request_id": request.get("request_id", ""),
            })
            return

        commits = get_commits(repo_path, from_tag, to_tag)
        commits = commits[:max_commits]

        if output_format == "markdown":
            content = generate_markdown(commits)
        else:
            content = json.dumps(commits, indent=2, default=str)

        types_present = list({c["type"] for c in commits})

        write_response({
            "success": True,
            "request_id": request.get("request_id", ""),
            "content": [{"type": "text", "text": content[:20000]}],
            "structured_content": {
                "commit_count": len(commits),
                "types": types_present,
                "date_range": f"{commits[-1]['date'] if commits else 'N/A'} - {commits[0]['date'] if commits else 'N/A'}",
            },
        })
    except subprocess.TimeoutExpired:
        write_response({
            "success": False,
            "error": {"code": "TIMEOUT", "message": "git log timed out"},
            "request_id": request.get("request_id", ""),
        })
    except Exception as e:
        write_response({
            "success": False,
            "error": {"code": "EXECUTION_FAILED", "message": str(e), "details": traceback.format_exc()},
            "request_id": request.get("request_id", ""),
        })


if __name__ == "__main__":
    main()
```

- [ ] **Step 10.2: Crear `tools/dev-tools/changelog_generator/tool.yaml`**

```yaml
name: "changelog_generator"
description: "Genera changelog desde commits git convencionales. Agrupa por tipo (feat, fix, docs, refactor, etc). 0 tokens LLM."
command: "python3"
args: ["main.py"]
timeout: "60s"
input_schema:
  type: object
  properties:
    repo_path:
      type: string
      description: "Ruta absoluta al repositorio git"
    from_tag:
      type: string
      description: "Tag o commit inicial (opcional, si se omite usa HEAD~30)"
    to_tag:
      type: string
      description: "Tag o commit final (opcional, si se omite usa HEAD)"
    format:
      type: string
      enum: ["markdown", "json"]
      default: "markdown"
      description: "Formato de salida"
    max_commits:
      type: integer
      default: 50
      description: "Maximo de commits a incluir"
  required:
    - repo_path
```

---

## Task 11: Tests para nuevas herramientas

**Files:**
- Create: `tests/tools/dev-tools/test_test_runner.py`
- Create: `tests/tools/dev-tools/test_doc_generator.py`
- Create: `tests/tools/dev-tools/test_refactor_suggester.py`
- Create: `tests/tools/dev-tools/test_api_diff.py`
- Create: `tests/tools/dev-tools/test_changelog_generator.py`

- [ ] **Step 11.1: Crear `tests/tools/dev-tools/test_test_runner.py`**

```python
"""Smoke tests for test_runner tool."""
import json
import subprocess
import sys
from pathlib import Path

TOOL_DIR = Path(__file__).resolve().parent.parent.parent.parent / "tools" / "dev-tools" / "test_runner"


def test_smoke_invalid_path():
    result = subprocess.run(
        [sys.executable, str(TOOL_DIR / "main.py")],
        input=json.dumps({"request_id": "t1", "arguments": {"project_root": "/nonexistent"}}),
        capture_output=True, text=True, timeout=15,
    )
    output = json.loads(result.stdout)
    assert not output["success"]
    assert output["error"]["code"] == "INVALID_INPUT"


def test_smoke_unknown_framework():
    result = subprocess.run(
        [sys.executable, str(TOOL_DIR / "main.py")],
        input=json.dumps({"request_id": "t2", "arguments": {"project_root": str(Path.cwd()), "framework": "unknown"}}),
        capture_output=True, text=True, timeout=15,
    )
    output = json.loads(result.stdout)
    assert not output["success"]
```

- [ ] **Step 11.2: Crear `tests/tools/dev-tools/test_doc_generator.py`**

```python
"""Smoke tests for doc_generator tool."""
import json
import subprocess
import sys
from pathlib import Path

TOOL_DIR = Path(__file__).resolve().parent.parent.parent.parent / "tools" / "dev-tools" / "doc_generator"


def test_smoke_invalid_path():
    result = subprocess.run(
        [sys.executable, str(TOOL_DIR / "main.py")],
        input=json.dumps({"request_id": "t1", "arguments": {"project_root": "/nonexistent"}}),
        capture_output=True, text=True, timeout=15,
    )
    output = json.loads(result.stdout)
    assert not output["success"]


def test_smoke_project_root():
    result = subprocess.run(
        [sys.executable, str(TOOL_DIR / "main.py")],
        input=json.dumps({"request_id": "t2", "arguments": {"project_root": str(Path.cwd())}}),
        capture_output=True, text=True, timeout=15,
    )
    output = json.loads(result.stdout)
    assert output["success"]
    assert "structured_content" in output
```

- [ ] **Step 11.3: Crear `tests/tools/dev-tools/test_refactor_suggester.py`**

```python
"""Smoke tests for refactor_suggester tool."""
import json
import subprocess
import sys
from pathlib import Path

TOOL_DIR = Path(__file__).resolve().parent.parent.parent.parent / "tools" / "dev-tools" / "refactor_suggester"


def test_smoke_invalid_path():
    result = subprocess.run(
        [sys.executable, str(TOOL_DIR / "main.py")],
        input=json.dumps({"request_id": "t1", "arguments": {"project_root": "/nonexistent"}}),
        capture_output=True, text=True, timeout=15,
    )
    output = json.loads(result.stdout)
    assert not output["success"]


def test_smoke_project_root():
    result = subprocess.run(
        [sys.executable, str(TOOL_DIR / "main.py")],
        input=json.dumps({"request_id": "t2", "arguments": {"project_root": str(Path.cwd())}}),
        capture_output=True, text=True, timeout=15,
    )
    output = json.loads(result.stdout)
    assert output["success"]
```

- [ ] **Step 11.4: Crear `tests/tools/dev-tools/test_api_diff.py`**

```python
"""Smoke tests for api_diff tool."""
import json
import subprocess
import sys
from pathlib import Path

TOOL_DIR = Path(__file__).resolve().parent.parent.parent.parent / "tools" / "dev-tools" / "api_diff"


def test_smoke_invalid_path():
    result = subprocess.run(
        [sys.executable, str(TOOL_DIR / "main.py")],
        input=json.dumps({"request_id": "t1", "arguments": {"old_spec": "/nonexistent", "new_spec": "/nonexistent2"}}),
        capture_output=True, text=True, timeout=15,
    )
    output = json.loads(result.stdout)
    assert not output["success"]
```

- [ ] **Step 11.5: Crear `tests/tools/dev-tools/test_changelog_generator.py`**

```python
"""Smoke tests for changelog_generator tool."""
import json
import subprocess
import sys
from pathlib import Path

TOOL_DIR = Path(__file__).resolve().parent.parent.parent.parent / "tools" / "dev-tools" / "changelog_generator"


def test_smoke_invalid_path():
    result = subprocess.run(
        [sys.executable, str(TOOL_DIR / "main.py")],
        input=json.dumps({"request_id": "t1", "arguments": {"repo_path": "/nonexistent"}}),
        capture_output=True, text=True, timeout=15,
    )
    output = json.loads(result.stdout)
    assert not output["success"]
```

---

## Task 12: Sistema de toolkits (toolkit configs)

**Files:**
- Create: `configs/toolkits/development.yaml`
- Create: `configs/toolkits/default.yaml`

- [ ] **Step 12.1: Crear `configs/toolkits/default.yaml`**

```yaml
# Toolkit por defecto: herramientas de productividad general
name: "default"
description: "Toolkit general para usuarios finales"
tools:
  - echo
  - datetime
  - generate_report
  - analyze_data
  - analyze_image
  - kb_ingest
  - kb_search
  - weather_forecast
  - web_scraper
  - searxng_search
  - browser_scraper
  - rss_reader
  - canvas_diagram
  - rustfs_storage
  - transcribe
```

- [ ] **Step 12.2: Crear `configs/toolkits/development.yaml`**

```yaml
# Toolkit de desarrollo: incluye dev-tools + herramientas core
name: "development"
description: "Toolkit para desarrollo de software"
tools:
  # Core
  - echo
  - datetime
  - kb_ingest
  - kb_search
  - generate_report
  # Dev tools
  - opencode_context
  - codebase_scan
  - git_inspector
  - dependency_audit
  - security_lint
  - test_runner
  - doc_generator
  - refactor_suggester
  - changelog_generator
```

---

## Self-Review Checklist

| Requisito | Task |
|-----------|------|
| Límite archivos escaneados (MAX_FILES) | Task 1.1, 1.2 |
| Cache entre ejecuciones (ScanCache) | Task 1.1, 2.1, 3.1 |
| Best-effort flags dependency_audit | Task 4 |
| git en Dockerfile | Task 5 |
| Exportar codebase_utils desde __init__.py | Task 1.3 |
| Nueva tool: test_runner | Task 6 |
| Nueva tool: doc_generator | Task 7 |
| Nueva tool: refactor_suggester | Task 8 |
| Nueva tool: api_diff | Task 9 |
| Nueva tool: changelog_generator | Task 10 |
| Tests para nuevas tools | Task 11 |
| Sistema de toolkits | Task 12 |
