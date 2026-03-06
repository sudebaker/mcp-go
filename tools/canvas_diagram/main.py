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
from typing import Any, Optional
from urllib.parse import urlparse

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

try:
    import requests
    REQUESTS_AVAILABLE = True
except ImportError:
    REQUESTS_AVAILABLE = False


DEFAULT_TIMEOUT = 10
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

LAYOUTS = ["horizontal", "vertical", "radial"]


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
            "options": {
                "num_ctx": 2048,
                "temperature": 0.1
            }
        }
        
        response = requests.post(
            f"{api_url}/api/generate",
            json=payload,
            timeout=30
        )
        
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


def parse_dsl_to_canvas(dsl: str, layout: str = "horizontal") -> dict:
    dsl = dsl.strip()
    
    nodes = []
    edges = []
    node_positions = {}
    
    token_pattern = r'(\([^)]+\)|\[[^\]]+\]|\{{[^}}]+\}}|[^->,\[\](){}#]+)(?:#(\w+))?'
    tokens = re.findall(token_pattern, dsl.replace('->', '->').replace(',', ','))
    
    nodes_dict = {}
    connections = []
    node_colors = {}
    node_types = {}
    
    current_nodes = []
    
    parts = re.split(r'->|→', dsl)
    
    for i, part in enumerate(parts):
        part = part.strip()
        if not part:
            continue
        
        branch_nodes = [n.strip() for n in part.split(',')]
        
        for node_str in branch_nodes:
            node_str = node_str.strip()
            if not node_str:
                continue
            
            color_match = re.search(r'#(\w+)$', node_str)
            color = None
            if color_match:
                color = color_match.group(1)
                node_str = node_str[:-(len(color)+1)].strip()
            
            node_id = None
            node_label = node_str
            node_type = "text"
            
            if node_str.startswith('(') and node_str.endswith(')'):
                node_label = node_str[1:-1]
                node_type = "start"
                if "end" in node_label.lower() or "fin" in node_label.lower():
                    node_type = "end"
            elif node_str.startswith('{{') and node_str.endswith('}}'):
                node_label = node_str[2:-2]
                node_type = "group"
            else:
                node_label = node_str
            
            node_id = f"node_{len(nodes_dict)}"
            
            nodes_dict[node_id] = {
                "label": node_label,
                "type": node_type,
                "color": color
            }
            
            if i > 0:
                for prev_node in current_nodes:
                    connections.append((prev_node, node_id))
            
            current_nodes = [node_id]
    
    node_width = 180
    node_height = 80
    horizontal_spacing = 250
    vertical_spacing = 150
    
    sorted_nodes = sorted(nodes_dict.keys(), key=lambda x: int(x.split('_')[1]))
    
    if layout == "horizontal":
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
            
            node_obj = {
                "id": node_id,
                "type": "text",
                "x": i * horizontal_spacing,
                "y": 100,
                "width": node_width,
                "height": node_height,
                "text": text_prefix + node["label"]
            }
            
            if color:
                node_obj["color"] = color
            
            nodes.append(node_obj)
            node_positions[node_id] = i
    
    elif layout == "vertical":
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
            
            node_obj = {
                "id": node_id,
                "type": "text",
                "x": 100,
                "y": i * vertical_spacing,
                "width": node_width,
                "height": node_height,
                "text": text_prefix + node["label"]
            }
            
            if color:
                node_obj["color"] = color
            
            nodes.append(node_obj)
            node_positions[node_id] = i
    
    elif layout == "radial":
        center_x = 400
        center_y = 300
        radius = 200
        
        if sorted_nodes:
            center_node = sorted_nodes[0]
            nodes_dict[center_node]["label"] = "● " + nodes_dict[center_node]["label"]
            nodes.append({
                "id": center_node,
                "type": "text",
                "x": center_x - node_width // 2,
                "y": center_y - node_height // 2,
                "width": node_width,
                "height": node_height,
                "text": nodes_dict[center_node]["label"]
            })
            node_positions[center_node] = 0
        
        for i, node_id in enumerate(sorted_nodes[1:], 1):
            angle = (2 * 3.14159 * i) / (len(sorted_nodes) - 1) - 3.14159 / 2
            x = center_x + radius * (1 if i % 2 == 0 else -1) * abs(0.5 - (i % 4) / 4)
            y = center_y + radius * 0.8 * (i % 2 == 0 and i % 4 != 0)
            
            node = nodes_dict[node_id]
            
            color = CANVAS_COLORS.get(node["color"], None)
            
            node_obj = {
                "id": node_id,
                "type": "text",
                "x": int(x),
                "y": int(y),
                "width": node_width,
                "height": node_height,
                "text": node["label"]
            }
            
            if color:
                node_obj["color"] = color
            
            nodes.append(node_obj)
            node_positions[node_id] = i
    
    edge_id = 0
    for from_node, to_node in connections:
        edge = {
            "id": f"edge_{edge_id}",
            "fromNode": from_node,
            "toNode": to_node,
            "fromSide": "right" if layout == "horizontal" else "bottom",
            "toSide": "left" if layout == "horizontal" else "top"
        }
        edges.append(edge)
        edge_id += 1
    
    return {
        "nodes": nodes,
        "edges": edges
    }


def generate_from_description(description: str, layout: str, llm_api_url: Optional[str], llm_model: str) -> dict:
    dsl = None
    
    if llm_api_url:
        prompt = generate_dsl_prompt(description)
        dsl = call_llm(llm_api_url, llm_model, prompt)
    
    if not dsl:
        dsl = description
    
    return parse_dsl_to_canvas(dsl, layout)


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
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "INVALID_INPUT",
                    "message": "description is required"
                }
            })
            return

        if layout not in LAYOUTS:
            layout = "horizontal"

        canvas_json = generate_from_description(description, layout, llm_api_url, llm_model)

        saved_path = None
        if save_path:
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
            output_dir = "/data/output"
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
        
        if saved_path:
            response_text += f"**Saved to:** `{saved_path}`\n\n"
        
        response_text += "**Preview:**\n" + "\n".join(preview_lines)
        
        response_text += f"\n\n**JSON Canvas (first 1000 chars):**\n```json\n{canvas_json_str[:1000]}..."

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [
                {
                    "type": "text",
                    "text": response_text
                }
            ],
            "structured_content": {
                "canvas_json": canvas_json,
                "layout": layout,
                "node_count": len(canvas_json.get("nodes", [])),
                "edge_count": len(canvas_json.get("edges", [])),
                "saved_path": saved_path,
                "used_llm": llm_api_url is not None
            }
        })

    except json.JSONDecodeError as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {
                "code": "INVALID_INPUT",
                "message": f"Failed to parse JSON input: {str(e)}"
            }
        })
    except Exception as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", "") if request else "",
            "error": {
                "code": "EXECUTION_FAILED",
                "message": str(e),
                "details": traceback.format_exc()
            }
        })


if __name__ == "__main__":
    main()
