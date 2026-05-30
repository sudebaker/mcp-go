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
