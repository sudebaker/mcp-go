#!/usr/bin/env python3
"""
Server Status Tool for MCP Orchestrator.
Reports CPU, memory, disk, uptime, load average and Docker container status.
"""

import json
import os
import socket
import sys
import time
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from common.structured_logging import get_logger

logger = get_logger(__name__, "server_status")


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


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
        return "N/A"


def get_load() -> str:
    try:
        with open("/proc/loadavg") as f:
            parts = f.read().split()
            return f"{parts[0]}, {parts[1]}, {parts[2]}"
    except Exception:
        return "N/A"


def get_cpu_usage() -> str:
    try:
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
        return "N/A"


def get_memory() -> dict:
    try:
        with open("/proc/meminfo") as f:
            lines = f.readlines()
        mem_info = {}
        for line in lines:
            parts = line.split()
            if len(parts) >= 2:
                key = parts[0].rstrip(":")
                try:
                    mem_info[key] = int(parts[1])
                except ValueError:
                    pass
        total = mem_info.get("MemTotal", 0) // 1024
        available = mem_info.get("MemAvailable", mem_info.get("MemFree", 0)) // 1024
        used = total - available
        pct = round(used / total * 100, 1) if total > 0 else 0
        return {"total_mb": total, "used_mb": used, "free_mb": available, "used_pct": pct}
    except Exception:
        return {}


def get_disk() -> list:
    results = []
    try:
        with open("/proc/mounts") as f:
            mounts = f.readlines()
        for line in mounts:
            parts = line.split()
            if len(parts) >= 4:
                mount = parts[1]
                if mount in ("/", "/home", "/data"):
                    try:
                        stat = os.statvfs(mount)
                        total_gb = stat.f_blocks * stat.f_frsize / (1024**3)
                        used_gb = (stat.f_blocks - stat.f_bfree) * stat.f_frsize / (1024**3)
                        avail_gb = stat.f_bavail * stat.f_frsize / (1024**3)
                        pct = round(used_gb / total_gb * 100, 1) if total_gb > 0 else 0
                        results.append({
                            "mount": mount,
                            "size": f"{total_gb:.1f}G",
                            "used": f"{used_gb:.1f}G",
                            "avail": f"{avail_gb:.1f}G",
                            "pct": f"{pct}%"
                        })
                    except Exception:
                        pass
    except Exception:
        pass
    return results[:5]


def get_docker_containers() -> list:
    try:
        import json as _json
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(5)
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
        logger.error(
            "Unhandled exception in server_status",
            extra_data={"error": str(e)}
        )
        write_response({
            "success": False,
            "request_id": request.get("request_id", "") if request else "",
            "error": {"code": "EXECUTION_FAILED", "message": str(e)}
        })


if __name__ == "__main__":
    main()
