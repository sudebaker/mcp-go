#!/usr/bin/env python3
"""
Server Status Tool for MCP Orchestrator.
Reports CPU, memory, disk, uptime, load average and Docker container status.
"""

import json
import sys
import os
import subprocess
import traceback
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from common.structured_logging import get_logger

logger = get_logger(__name__, "server_status")


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


def run(cmd: str) -> str:
    try:
        return subprocess.check_output(cmd, shell=True, stderr=subprocess.DEVNULL, text=True).strip()
    except Exception:
        return "N/A"


def get_uptime() -> str:
    try:
        with open("/proc/uptime") as f:
            secs = float(f.read().split()[0])
        days = int(secs // 86400)
        hours = int((secs % 86400) // 3600)
        mins = int((secs % 3600) // 60)
        parts = []
        if days: parts.append(f"{days}d")
        if hours: parts.append(f"{hours}h")
        parts.append(f"{mins}m")
        return " ".join(parts)
    except Exception:
        return run("uptime -p")


def get_load() -> str:
    return run("cat /proc/loadavg | awk '{print $1\", \"$2\", \"$3}'")


def get_cpu_usage() -> str:
    try:
        # Two reads of /proc/stat with 0.5s gap for accurate CPU%
        import time
        def read_cpu():
            with open("/proc/stat") as f:
                line = f.readline()
            vals = list(map(int, line.split()[1:]))
            idle = vals[3]
            total = sum(vals)
            return idle, total
        i1, t1 = read_cpu()
        time.sleep(0.5)
        i2, t2 = read_cpu()
        idle_delta = i2 - i1
        total_delta = t2 - t1
        if total_delta == 0:
            return "0%"
        used_pct = round((1 - idle_delta / total_delta) * 100, 1)
        return f"{used_pct}%"
    except Exception:
        return run("top -bn1 | grep 'Cpu(s)' | awk '{print $2+$4\"%\"}'")


def get_memory() -> dict:
    raw = run("free -m | awk 'NR==2{print $2, $3, $4}'")
    parts = raw.split()
    if len(parts) == 3:
        total, used, free = int(parts[0]), int(parts[1]), int(parts[2])
        pct = round(used / total * 100, 1) if total > 0 else 0
        return {"total_mb": total, "used_mb": used, "free_mb": free, "used_pct": pct}
    return {}


def get_disk() -> list:
    raw = run("df -h --output=target,size,used,avail,pcent | tail -n +2 | grep -E '^(/|/home|/data)' | head -5")
    results = []
    for line in raw.splitlines():
        parts = line.split()
        if len(parts) >= 5:
            results.append({"mount": parts[0], "size": parts[1], "used": parts[2], "avail": parts[3], "pct": parts[4]})
    return results


def get_docker_containers() -> list:
    # Use Docker API via unix socket directly (no CLI needed)
    try:
        import socket
        import json as _json
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.connect("/var/run/docker.sock")
        request = b"GET /containers/json HTTP/1.0\r\nHost: localhost\r\n\r\n"
        sock.sendall(request)
        response = b""
        while True:
            chunk = sock.recv(4096)
            if not chunk:
                break
            response += chunk
        sock.close()
        # Split headers from body
        body = response.split(b"\r\n\r\n", 1)[1]
        containers_raw = _json.loads(body)
        containers = []
        for c in containers_raw:
            name = c.get("Names", [""])[0].lstrip("/")
            status = c.get("Status", "")
            image = c.get("Image", "")
            containers.append({"name": name, "status": status, "image": image})
        return containers
    except Exception as e:
        return [{"name": "error", "status": str(e), "image": ""}]


def format_report(data: dict) -> str:
    lines = ["**📊 Estado del Servidor**\n"]

    # Uptime & Load
    lines.append(f"⏱️ **Uptime:** {data.get('uptime', 'N/A')}")
    lines.append(f"⚡ **Load avg (1/5/15m):** {data.get('load', 'N/A')}")
    lines.append(f"🖥️ **CPU uso:** {data.get('cpu', 'N/A')}")

    # Memory
    mem = data.get("memory", {})
    if mem:
        bar = "█" * int(mem['used_pct'] / 10) + "░" * (10 - int(mem['used_pct'] / 10))
        lines.append(f"🧠 **RAM:** {mem['used_mb']}MB / {mem['total_mb']}MB ({mem['used_pct']}%) [{bar}]")

    # Disk
    lines.append("\n💾 **Disco:**")
    for d in data.get("disk", []):
        lines.append(f"  • `{d['mount']}` — {d['used']}/{d['size']} ({d['pct']}) libre: {d['avail']}")

    # Docker
    containers = data.get("containers", [])
    lines.append(f"\n🐳 **Docker ({len(containers)} contenedores activos):**")
    for c in containers:
        status_icon = "✅" if "Up" in c["status"] else "⚠️"
        lines.append(f"  {status_icon} `{c['name']}` — {c['status']}")

    return "\n".join(lines)


def main() -> None:
    request = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")

        data = {
            "uptime": get_uptime(),
            "load": get_load(),
            "cpu": get_cpu_usage(),
            "memory": get_memory(),
            "disk": get_disk(),
            "containers": get_docker_containers(),
        }

        report = format_report(data)

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": report}],
            "structured_content": data
        })

    except json.JSONDecodeError as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "INVALID_INPUT", "message": str(e)}
        })
    except Exception as e:
        logger.error("Unhandled exception in server_status", extra_data={"error": str(e), "traceback": traceback.format_exc()})
        write_response({
            "success": False,
            "request_id": request.get("request_id", "") if request else "",
            "error": {"code": "EXECUTION_FAILED", "message": str(e)}
        })


if __name__ == "__main__":
    main()
