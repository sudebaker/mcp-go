#!/usr/bin/env python3
"""
Canvas Diagram Generator for MCP Orchestrator.
Generates Obsidian Canvas JSON format from text descriptions.
Uses LLM if available, falls back to simple DSL parser.
"""

import json
import sys
import os
import re
import uuid
import traceback
import io
from datetime import timedelta
from typing import Any, Optional
from urllib.parse import urlparse

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.structured_logging import get_logger

logger = get_logger(__name__, "canvas_diagram")

try:
    import requests

    REQUESTS_AVAILABLE = True
except ImportError:
    REQUESTS_AVAILABLE = False

try:
    from minio import Minio
    from minio.error import S3Error

    MINIO_AVAILABLE = True
except ImportError:
    MINIO_AVAILABLE = False
    S3Error = Exception


DEFAULT_TIMEOUT = 10
RUSTFS_BUCKET = "diagramas"
CANVAS_COLORS = {
    "red": "1",
    "orange": "2",
    "yellow": "3",
    "green": "4",
    "cyan": "5",
    "purple": "6",
    "1": "1",
    "2": "2",
    "3": "3",
    "4": "4",
    "5": "5",
    "6": "6",
}

LAYOUTS = ["horizontal", "vertical", "radial", "auto"]
ALLOWED_OUTPUT_DIR = "/data/output"


def get_rustfs_client() -> Optional[Minio]:
    if not MINIO_AVAILABLE:
        return None

    endpoint = os.environ.get("RUSTFS_ENDPOINT", "rustfs:9000")
    access_key = os.environ.get("RUSTFS_ACCESS_KEY_ID")
    secret_key = os.environ.get("RUSTFS_SECRET_ACCESS_KEY")
    use_ssl = os.environ.get("RUSTFS_USE_SSL", "false").lower() == "true"

    if not access_key or not secret_key:
        logger.error(
            "Missing RustFS credentials",
            extra_data={
                "missing": [
                    k
                    for k, v in {
                        "RUSTFS_ACCESS_KEY_ID": access_key,
                        "RUSTFS_SECRET_ACCESS_KEY": secret_key,
                    }.items()
                    if not v
                ]
            },
        )
        return None

    try:
        client = Minio(
            endpoint, access_key=access_key, secret_key=secret_key, secure=use_ssl
        )
        return client
    except Exception:
        return None


def upload_to_rustfs(client: Minio, bucket: str, key: str, content: str) -> dict:
    try:
        if not client.bucket_exists(bucket):
            client.make_bucket(bucket)

        data = io.BytesIO(content.encode("utf-8"))
        client.put_object(
            bucket, key, data, length=len(content), content_type="application/json"
        )

        presigned_url = client.presigned_get_object(
            bucket, key, expires=timedelta(hours=72)
        )

        return {"success": True, "url": presigned_url, "key": key, "bucket": bucket}
    except Exception as e:
        return {"success": False, "error": str(e)}


def validate_save_path(path: str) -> tuple[bool, Optional[str]]:
    if not path:
        return False, "Save path is required"

    abs_path = os.path.abspath(path)
    abs_allowed = os.path.abspath(ALLOWED_OUTPUT_DIR)

    if not abs_path.startswith(abs_allowed):
        return False, f"Path must be within {ALLOWED_OUTPUT_DIR}"

    if ".." in path:
        return False, "Path traversal not allowed"

    return True, None


def read_request() -> dict[str, Any]:
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def call_llm(api_url: str, model: str, prompt: str) -> Optional[str]:
    if not REQUESTS_AVAILABLE:
        return None

    try:
        payload = {
            "model": model,
            "prompt": prompt,
            "stream": False,
            "options": {"num_ctx": 2048, "temperature": 0.1},
        }

        response = requests.post(f"{api_url}/api/generate", json=payload, timeout=30)

        if response.status_code == 200:
            result = response.json()
            return result.get("response", "").strip()

        return None
    except Exception:
        return None


def generate_dsl_prompt(description: str) -> str:
    return f"""Convert the following description into a simple DSL for a diagram.
Return ONLY the DSL, no explanation.

DSL syntax:
- Use -> for connections: A -> B -> C
- Use , for branches: A -> B, A -> C
- Use [text] for node labels: A[Usuario] -> B[Login]
- Use (text) for start/end nodes: (Inicio) -> A
- Use {{label}} for groups: {{Admin Area}}
- Use #color for colors: A#red -> B#green

Description: {description}

DSL:"""


def calculate_node_size(text: str) -> tuple[int, int]:
    lines = text.count("\n") + 1
    width = max(180, min(len(text) * 8 + 40, 400))
    height = max(80, min(lines * 24 + 30, 200))
    return width, height


def suggest_layout(
    node_count: int, max_branches: int, depth: int, connections: list
) -> str:
    if node_count <= 5 and max_branches <= 2:
        return "horizontal"
    elif depth > 6 or max_branches > 4:
        return "radial"
    else:
        return "vertical"


def parse_dsl_structure(dsl: str) -> dict:
    nodes_dict = {}
    connections = []
    node_parents = {}

    parts = re.split(r"->|→", dsl)

    for i, part in enumerate(parts):
        part = part.strip()
        if not part:
            continue

        branch_nodes = [n.strip() for n in part.split(",")]

        for node_str in branch_nodes:
            node_str = node_str.strip()
            if not node_str:
                continue

            color_match = re.search(r"#(\w+)$", node_str)
            color = None
            if color_match:
                color = color_match.group(1)
                node_str = node_str[: -(len(color) + 1)].strip()

            node_label = node_str
            node_type = "text"

            if node_str.startswith("(") and node_str.endswith(")"):
                node_label = node_str[1:-1]
                node_type = "start"
                if "end" in node_label.lower() or "fin" in node_label.lower():
                    node_type = "end"
            elif node_str.startswith("{{") and node_str.endswith("}}"):
                node_label = node_str[2:-2]
                node_type = "group"

            node_id = f"node_{len(nodes_dict)}"

            nodes_dict[node_id] = {
                "label": node_label,
                "type": node_type,
                "color": color,
                "depth": i,
            }

            if i > 0:
                for prev_node in branch_nodes[:1]:
                    prev_id = f"node_{len(nodes_dict) - len(branch_nodes) - 1 + branch_nodes.index(node_str)}"
                    if prev_id in nodes_dict:
                        connections.append((prev_id, node_id))
                        if node_id not in node_parents:
                            node_parents[node_id] = []
                        node_parents[node_id].append(prev_id)

    return {
        "nodes": nodes_dict,
        "connections": connections,
        "node_parents": node_parents,
    }


def calculate_node_depths(nodes_dict: dict, connections: list) -> dict:
    depths = {}
    for node_id in nodes_dict:
        depths[node_id] = 0

    for from_node, to_node in connections:
        from_depth = nodes_dict[from_node].get("depth", 0)
        if to_node in depths:
            depths[to_node] = max(depths[to_node], from_depth + 1)
        else:
            depths[to_node] = from_depth + 1

    for node_id, conns in connections:
        if node_id not in depths:
            depths[node_id] = 0

    return depths


def parse_dsl_to_canvas(dsl: str, layout: str = "horizontal") -> dict:
    dsl = dsl.strip()

    nodes = []
    edges = []

    nodes_dict = {}
    connections = []
    previous_nodes = []
    node_id_counter = 0

    parts = re.split(r"->|→", dsl)

    for i, part in enumerate(parts):
        part = part.strip()
        if not part:
            continue

        branch_nodes = [n.strip() for n in part.split(",") if n.strip()]

        if not branch_nodes:
            continue

        current_level_nodes = []

        for node_str in branch_nodes:
            color_match = re.search(r"#(\w+)$", node_str)
            color = None
            if color_match:
                color = color_match.group(1)
                node_str = node_str[: -(len(color) + 1)].strip()

            node_label = node_str
            node_type = "text"

            if node_str.startswith("(") and node_str.endswith(")"):
                node_label = node_str[1:-1]
                node_type = "start"
                if "end" in node_label.lower() or "fin" in node_label.lower():
                    node_type = "end"
            elif node_str.startswith("{{") and node_str.endswith("}}"):
                node_label = node_str[2:-2]
                node_type = "group"

            existing_node_id = None
            for nid, ndata in nodes_dict.items():
                if ndata["label"] == node_label:
                    existing_node_id = nid
                    break

            if existing_node_id:
                node_id = existing_node_id
            else:
                node_id = f"node_{node_id_counter}"
                node_id_counter += 1
                nodes_dict[node_id] = {
                    "label": node_label,
                    "type": node_type,
                    "color": color,
                    "depth": i,
                }

            if i > 0 and previous_nodes:
                for prev_node in previous_nodes:
                    if (prev_node, node_id) not in connections:
                        connections.append((prev_node, node_id))

            current_level_nodes.append(node_id)

        previous_nodes = current_level_nodes

    node_widths = {}
    node_heights = {}
    for node_id, node in nodes_dict.items():
        w, h = calculate_node_size(node["label"])
        node_widths[node_id] = w
        node_heights[node_id] = h

    max_branches = 1
    for node_id in nodes_dict:
        branch_count = sum(1 for c in connections if c[1] == node_id)
        max_branches = max(max_branches, branch_count)

    depth = len(parts)

    if layout == "auto":
        layout = suggest_layout(len(nodes_dict), max_branches, depth, connections)

    sorted_nodes = sorted(
        nodes_dict.keys(), key=lambda x: nodes_dict[x].get("depth", 0)
    )

    if layout == "horizontal":
        x_positions = [0]
        for i, node_id in enumerate(sorted_nodes[:-1]):
            x_positions.append(x_positions[-1] + node_widths.get(node_id, 180) + 80)

        y_offset = 100
        depth_positions = {}

        for i, node_id in enumerate(sorted_nodes):
            node = nodes_dict[node_id]

            color = CANVAS_COLORS.get(node["color"], None)
            if node["type"] == "start" and not color:
                color = "4"
            elif node["type"] == "end" and not color:
                color = "1"

            text_prefix = ""
            if node["type"] == "start":
                text_prefix = "🔵 "
            elif node["type"] == "end":
                text_prefix = "⏹ "

            depth_key = node.get("depth", 0)
            depth_positions[depth_key] = depth_positions.get(depth_key, -1) + 1
            y = y_offset + depth_positions[depth_key] * 120

            node_width = node_widths.get(node_id, 180)
            node_height = node_heights.get(node_id, 80)

            node_obj = {
                "id": node_id,
                "type": "text",
                "x": x_positions[i] if i < len(x_positions) else i * 260,
                "y": y,
                "width": node_width,
                "height": node_height,
                "text": text_prefix + node["label"],
            }

            if color:
                node_obj["color"] = color

            nodes.append(node_obj)

    elif layout == "vertical":
        y_positions = [0]
        for i, node_id in enumerate(sorted_nodes[:-1]):
            y_positions.append(y_positions[-1] + node_heights.get(node_id, 80) + 80)

        x_offset = 100
        depth_positions = {}

        for i, node_id in enumerate(sorted_nodes):
            node = nodes_dict[node_id]

            color = CANVAS_COLORS.get(node["color"], None)
            if node["type"] == "start" and not color:
                color = "4"
            elif node["type"] == "end" and not color:
                color = "1"

            text_prefix = ""
            if node["type"] == "start":
                text_prefix = "🔵 "
            elif node["type"] == "end":
                text_prefix = "⏹ "

            depth_key = node.get("depth", 0)
            depth_positions[depth_key] = depth_positions.get(depth_key, -1) + 1
            x = x_offset + depth_positions[depth_key] * 220

            node_width = node_widths.get(node_id, 180)
            node_height = node_heights.get(node_id, 80)

            node_obj = {
                "id": node_id,
                "type": "text",
                "x": x,
                "y": y_positions[i] if i < len(y_positions) else i * 160,
                "width": node_width,
                "height": node_height,
                "text": text_prefix + node["label"],
            }

            if color:
                node_obj["color"] = color

            nodes.append(node_obj)

    elif layout == "radial":
        center_x = 500
        center_y = 400

        root_nodes = [n for n in sorted_nodes if nodes_dict[n].get("depth", 0) == 0]

        if root_nodes:
            root_node = root_nodes[0]
            nodes_dict[root_node]["label"] = "● " + nodes_dict[root_node]["label"]

            root_width = node_widths.get(root_node, 180)
            root_height = node_heights.get(root_node, 80)
            nodes.append(
                {
                    "id": root_node,
                    "type": "text",
                    "x": center_x - root_width // 2,
                    "y": center_y - root_height // 2,
                    "width": root_width,
                    "height": root_height,
                    "text": nodes_dict[root_node]["label"],
                }
            )

        levels = {}
        for node_id in sorted_nodes:
            d = nodes_dict[node_id].get("depth", 1)
            if d not in levels:
                levels[d] = []
            levels[d].append(node_id)

        radius_step = 180
        for level, node_list in levels.items():
            if level == 0:
                continue
            radius = level * radius_step
            angle_step = 2 * 3.14159 / len(node_list) if len(node_list) > 0 else 1
            start_angle = -3.14159 / 2

            for i, node_id in enumerate(node_list):
                node = nodes_dict[node_id]
                angle = start_angle + i * angle_step

                node_width = node_widths.get(node_id, 180)
                node_height = node_heights.get(node_id, 80)

                x = center_x + int(
                    radius * 1.2 * (1 if i % 2 == 0 else -0.8) * abs(0.5 - (i % 4) / 4)
                )
                y = center_y + int(radius * 0.9 * (1 if i % 3 == 0 else -1))

                color = CANVAS_COLORS.get(node.get("color"), None)

                node_obj = {
                    "id": node_id,
                    "type": "text",
                    "x": x,
                    "y": y,
                    "width": node_width,
                    "height": node_height,
                    "text": node["label"],
                }

                if color:
                    node_obj["color"] = color

                nodes.append(node_obj)

    edge_id = 0
    for from_node, to_node in connections:
        edge = {
            "id": f"edge_{edge_id}",
            "fromNode": from_node,
            "toNode": to_node,
            "fromSide": "right" if layout == "horizontal" else "bottom",
            "toSide": "left" if layout == "horizontal" else "top",
        }
        edges.append(edge)
        edge_id += 1

    return {"nodes": nodes, "edges": edges, "layout": layout}


def generate_from_description(
    description: str, layout: str, llm_api_url: Optional[str], llm_model: str
) -> tuple[dict, str]:
    dsl = None

    if llm_api_url:
        prompt = generate_dsl_prompt(description)
        dsl = call_llm(llm_api_url, llm_model, prompt)

    if not dsl:
        dsl = description

    if layout == "auto" or not layout:
        layout = "auto"

    canvas_json = parse_dsl_to_canvas(dsl, layout)
    final_layout = canvas_json.get("layout", layout)

    return canvas_json, final_layout


def main() -> None:
    request = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})
        context = request.get("context", {})

        description = arguments.get("description", "")
        layout = arguments.get("layout", "horizontal")
        save_path = arguments.get("save_path", "")

        llm_api_url = context.get("llm_api_url")
        llm_model = context.get("llm_model", "llama3")

        if not description:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_INPUT",
                        "message": "description is required",
                    },
                }
            )
            return

        if layout not in LAYOUTS:
            layout = "horizontal"

        canvas_json, final_layout = generate_from_description(
            description, layout, llm_api_url, llm_model
        )

        rustfs_url = None
        saved_path = None

        client = get_rustfs_client()
        if client:
            filename = f"canvas_{uuid.uuid4().hex[:8]}.canvas"
            key = f"diagrams/{filename}"
            content = json.dumps(canvas_json, indent=2)
            result = upload_to_rustfs(client, RUSTFS_BUCKET, key, content)
            if result.get("success"):
                rustfs_url = result.get("url")

        if not rustfs_url:
            if save_path:
                is_valid, err = validate_save_path(save_path)
                if is_valid:
                    try:
                        save_dir = os.path.dirname(save_path)
                        if save_dir:
                            os.makedirs(save_dir, exist_ok=True)
                        with open(save_path, "w") as f:
                            json.dump(canvas_json, f, indent=2)
                        saved_path = save_path
                    except Exception:
                        pass

            if not saved_path:
                output_dir = ALLOWED_OUTPUT_DIR
                try:
                    os.makedirs(output_dir, exist_ok=True)
                    filename = f"canvas_{uuid.uuid4().hex[:8]}.canvas"
                    save_path = os.path.join(output_dir, filename)
                    with open(save_path, "w") as f:
                        json.dump(canvas_json, f, indent=2)
                    saved_path = save_path
                except Exception:
                    saved_path = None

        canvas_json_str = json.dumps(canvas_json, indent=2)

        preview_nodes = canvas_json.get("nodes", [])[:5]
        preview_lines = []
        for node in preview_nodes:
            text = node.get("text", "")[:50]
            preview_lines.append(f"- {text}")

        response_text = f"**Canvas Diagram Generated**\n\n"
        response_text += f"**Layout:** {layout}\n"
        response_text += f"**Nodes:** {len(canvas_json.get('nodes', []))}\n"
        response_text += f"**Connections:** {len(canvas_json.get('edges', []))}\n\n"

        if rustfs_url:
            response_text += f"**Saved to (RustFS):** {rustfs_url}\n\n"
        elif saved_path:
            response_text += f"**Saved to:** `{saved_path}`\n\n"

        response_text += "**Preview:**\n" + "\n".join(preview_lines)

        response_text += f"\n\n**JSON Canvas (first 1000 chars):**\n```json\n{canvas_json_str[:1000]}..."

        write_response(
            {
                "success": True,
                "request_id": request_id,
                "content": [{"type": "text", "text": response_text}],
                "structured_content": {
                    "canvas_json": canvas_json,
                    "layout": final_layout,
                    "node_count": len(canvas_json.get("nodes", [])),
                    "edge_count": len(canvas_json.get("edges", [])),
                    "rustfs_url": rustfs_url,
                    "local_path": saved_path,
                    "used_llm": llm_api_url is not None,
                },
            }
        )

    except json.JSONDecodeError as e:
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", ""),
                "error": {
                    "code": "INVALID_INPUT",
                    "message": f"Failed to parse JSON input: {str(e)}",
                },
            }
        )
    except Exception as e:
        logger.error(
            "Unhandled exception in canvas_diagram",
            extra_data={"error": str(e), "traceback": traceback.format_exc()},
        )
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", "") if request else "",
                "error": {
                    "code": "EXECUTION_FAILED",
                    "message": str(e),
                },
            }
        )


if __name__ == "__main__":
    main()
