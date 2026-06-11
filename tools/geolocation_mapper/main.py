#!/usr/bin/env python3
"""
Geolocation Mapper Tool for MCP Orchestrator.

Generates an HTML map with geographic points from IPs (resolved via ipinfo.io),
direct GPS coordinates, or mobile cell towers. Uses folium for rendering.
"""

import json
import os
import sys
import urllib.request
import urllib.error
from typing import Any

try:
    import folium
    FOLIUM_AVAILABLE = True
except ImportError:
    FOLIUM_AVAILABLE = False


def read_request() -> dict[str, Any]:
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    print(json.dumps(response, default=str))


IPINFO_URL = "https://ipinfo.io/{ip}/json"
IPINFO_TIMEOUT = 10

COLOR_MAP = {
    "red": "red",
    "blue": "blue",
    "green": "green",
    "orange": "orange",
    "purple": "purple",
    "darkblue": "darkblue",
    "lightred": "lightred",
    "beige": "beige",
    "darkgreen": "darkgreen",
    "pink": "pink",
}


def resolve_ip(ip: str) -> dict[str, Any]:
    try:
        url = IPINFO_URL.format(ip=ip)
        req = urllib.request.Request(url, headers={"User-Agent": "MCP-Forensic-Tool/1.0"})
        with urllib.request.urlopen(req, timeout=IPINFO_TIMEOUT) as resp:
            data = json.loads(resp.read().decode())
            loc = data.get("loc", "")
            if loc and "," in loc:
                lat_str, lon_str = loc.split(",", 1)
                return {
                    "lat": float(lat_str),
                    "lon": float(lon_str),
                    "city": data.get("city", ""),
                    "region": data.get("region", ""),
                    "country": data.get("country", ""),
                    "org": data.get("org", ""),
                    "ip": ip,
                }
        return {"error": f"No se pudieron resolver coordenadas para IP: {ip}", "ip": ip}
    except urllib.error.HTTPError as e:
        return {"error": f"ipinfo.io HTTP {e.code} para IP {ip}", "ip": ip}
    except urllib.error.URLError as e:
        return {"error": f"ipinfo.io sin conexión para IP {ip}: {e.reason}", "ip": ip}
    except Exception as e:
        return {"error": f"Error resolviendo IP {ip}: {str(e)}", "ip": ip}


def create_map(
    resolved_points: list[dict[str, Any]],
    output_path: str,
) -> tuple[str, dict[str, Any]]:
    if not resolved_points:
        raise ValueError("No hay puntos válidos para renderizar")

    lats = [p["lat"] for p in resolved_points if "lat" in p]
    lons = [p["lon"] for p in resolved_points if "lon" in p]

    if not lats or not lons:
        raise ValueError("No se pudieron determinar coordenadas para ningún punto")

    center_lat = sum(lats) / len(lats)
    center_lon = sum(lons) / len(lons)

    m = folium.Map(location=[center_lat, center_lon], zoom_start=5)

    for pt in resolved_points:
        if "lat" not in pt or "lon" not in pt:
            continue

        popup_text = f"<b>{pt.get('label', 'Punto')}</b><br>"
        popup_text += f"Lat: {pt['lat']}, Lon: {pt['lon']}<br>"

        if pt.get("type") == "ip":
            popup_text += f"IP: {pt.get('ip', '')}<br>"
            popup_text += f"Ciudad: {pt.get('city', '')}<br>"
            popup_text += f"Región: {pt.get('region', '')}<br>"
            popup_text += f"País: {pt.get('country', '')}<br>"
            popup_text += f"ISP: {pt.get('org', '')}<br>"

        color = COLOR_MAP.get(pt.get("color", "red"), "red")

        folium.Marker(
            location=[pt["lat"], pt["lon"]],
            popup=folium.Popup(popup_text, max_width=300),
            icon=folium.Icon(color=color, icon="info-sign"),
        ).add_to(m)

    os.makedirs(os.path.dirname(output_path) or ".", exist_ok=True)
    m.save(output_path)

    stats = {
        "total_points": len(resolved_points),
        "center": {"lat": round(center_lat, 4), "lon": round(center_lon, 4)},
        "bounds": {
            "min_lat": round(min(lats), 4),
            "max_lat": round(max(lats), 4),
            "min_lon": round(min(lons), 4),
            "max_lon": round(max(lons), 4),
        },
    }
    return output_path, stats


def main() -> None:
    if not FOLIUM_AVAILABLE:
        write_response({
            "success": False,
            "request_id": "",
            "error": {"code": "DEPENDENCY_MISSING", "message": "folium no está instalado"},
        })
        return

    request: dict[str, Any] = {}
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        points = arguments.get("points", [])
        if not isinstance(points, list) or len(points) == 0:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "INVALID_INPUT", "message": "'points' debe ser un array no vacío"},
            })
            return

        output_path = arguments.get("output_path", "/data/output/map.html")
        max_points = min(int(arguments.get("max_points", 100)), 500)

        valid_types = {"ip", "gps", "cell"}
        resolved: list[dict[str, Any]] = []
        errors: list[str] = []

        for i, pt in enumerate(points[:max_points]):
            if not isinstance(pt, dict):
                errors.append(f"points[{i}]: debe ser un objeto")
                continue

            pt_type = pt.get("type", "")
            if pt_type not in valid_types:
                errors.append(f"points[{i}]: type inválido '{pt_type}'")
                continue

            label = pt.get("label", f"Punto {i + 1}")
            color = pt.get("color", "red")

            if pt_type == "gps":
                lat = pt.get("lat")
                lon = pt.get("lon")
                if lat is None or lon is None:
                    errors.append(f"points[{i}]: GPS requiere lat y lon")
                    continue
                resolved.append({
                    "type": "gps",
                    "lat": float(lat),
                    "lon": float(lon),
                    "label": label,
                    "color": color,
                })

            elif pt_type == "ip":
                ip = pt.get("value", "")
                if not ip:
                    errors.append(f"points[{i}]: IP requiere 'value'")
                    continue
                ip_data = resolve_ip(ip)
                if "error" in ip_data:
                    errors.append(f"points[{i}]: {ip_data['error']}")
                    continue
                resolved.append({
                    "type": "ip",
                    "lat": ip_data["lat"],
                    "lon": ip_data["lon"],
                    "label": label,
                    "color": color,
                    "ip": ip_data.get("ip", ip),
                    "city": ip_data.get("city", ""),
                    "region": ip_data.get("region", ""),
                    "country": ip_data.get("country", ""),
                    "org": ip_data.get("org", ""),
                })

            elif pt_type == "cell":
                lat = pt.get("lat")
                lon = pt.get("lon")
                if lat is None or lon is None:
                    errors.append(f"points[{i}]: cell requiere lat y lon")
                    continue
                resolved.append({
                    "type": "cell",
                    "lat": float(lat),
                    "lon": float(lon),
                    "label": label,
                    "color": color,
                })

        if not resolved:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "EXECUTION_FAILED", "message": "No se pudieron resolver puntos válidos"},
            })
            return

        output_path, stats = create_map(resolved, output_path)

        lines = [
            "**Geolocation Mapper**",
            "",
            f"**Puntos totales:** {len(resolved)}",
            f"**Centro del mapa:** {stats['center']['lat']}, {stats['center']['lon']}",
            f"**Bounds:** {stats['bounds']}",
            f"**Mapa HTML:** {output_path}",
        ]

        if errors:
            lines.append(f"\n**⚠️ Errores ({len(errors)}):**")
            for err in errors[:10]:
                lines.append(f"- {err}")

        lines.append("\n**Puntos:**")
        lines.append("| # | Tipo | Label | Lat | Lon | Info |")
        lines.append("|---|------|-------|-----|-----|------|")
        for idx, pt in enumerate(resolved[:30], 1):
            extra = ""
            if pt.get("type") == "ip":
                extra = f"{pt.get('city', '')}, {pt.get('country', '')}"
            lines.append(f"| {idx} | {pt['type']} | {pt['label'][:30]} | {pt['lat']} | {pt['lon']} | {extra} |")
        if len(resolved) > 30:
            lines.append(f"| ... y {len(resolved) - 30} más |")

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": "\n".join(lines)}],
            "structured_content": {
                "map_path": output_path,
                "total_points": len(resolved),
                "stats": stats,
                "points": resolved[:200],
                "errors": errors if errors else None,
            },
        })

    except json.JSONDecodeError:
        write_response({
            "success": False,
            "request_id": "",
            "error": {"code": "INVALID_INPUT", "message": "Error al parsear el JSON de entrada"},
        })
    except ValueError as exc:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "INVALID_INPUT", "message": str(exc)},
        })
    except Exception as exc:
        write_response({
            "success": False,
            "request_id": request.get("request_id", ""),
            "error": {"code": "EXECUTION_FAILED", "message": str(exc)},
        })


if __name__ == "__main__":
    main()
