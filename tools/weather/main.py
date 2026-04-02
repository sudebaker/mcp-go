#!/usr/bin/env python3
"""
Weather Forecast Tool for MCP Orchestrator.

Retrieves weather forecasts from AEMET (Agencia Estatal de Meteorología),
Spain's official meteorological service. Provides comparative forecasts for
multiple Spanish locations including temperature, sky state, precipitation
probability, wind, and UV index.

Environment Variables:
    - AEMET_API_KEY: Optional API key for authenticated requests

Input Schema:
    - urls: Optional list of AEMET XML URLs (defaults to urls.txt)
    - max_days: Number of forecast days (1-7, default 3)
"""

import json
import os
import sys
import time
import random
import urllib.request
import xml.etree.ElementTree as ET
from datetime import datetime, date
from typing import Any

TOOL_DIR = os.path.dirname(os.path.abspath(__file__))
DEFAULT_URLS_FILE = os.path.join(TOOL_DIR, "urls.txt")


def read_request() -> dict[str, Any]:
    """Read JSON request from standard input.

    Parses the MCP protocol request containing request_id, arguments,
    and optional context information.

    Returns:
        Dictionary with request_id, arguments, and context keys.
    """
    return json.loads(sys.stdin.read())


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to standard output.

    Serializes the response dictionary as JSON and prints it.
    Uses default=str for datetime serialization compatibility.

    Args:
        response: Dictionary containing success status, request_id,
                  content, and optional structured_content.
    """
    print(json.dumps(response, default=str))


def fetch_xml(url: str) -> str | None:
    """Fetch XML weather data from AEMET API.

    Retrieves the XML forecast data for a single location. Implements
    rate limiting (1-5 seconds random delay) to be respectful to the
    AEMET service.

    Args:
        url: The AEMET XML URL for a specific location.

    Returns:
        Raw XML string on success, None on any failure.
        The XML uses ISO-8859-15 encoding.
    """
    try:
        # Sleep random time between 1 and 5 seconds to be polite to AEMET
        time.sleep(random.uniform(1, 5))

        req = urllib.request.Request(
            url,
            headers={
                "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
                "Accept": "application/xml, text/xml, */*"
            }
        )
        with urllib.request.urlopen(req, timeout=10) as response:
            return response.read().decode("iso-8859-15")
    except Exception:
        return None


def parse_location_xml(xml_content: str, url: str) -> dict[str, Any]:
    """Parse AEMET XML forecast data into a structured dictionary.

    Extracts location metadata and daily forecast information from the
    AEMET XML response format.

    Args:
        xml_content: Raw XML string from AEMET API.
        url: The source URL (for error reporting).

    Returns:
        Dictionary with keys:
            - localidad: Location name (string)
            - url: Source URL (string)
            - dias: List of daily forecasts (list of dicts)

    Raises:
        ET.ParseError: If XML is malformed (caught and handled gracefully).
    """
    try:
        root = ET.fromstring(xml_content)
        nombre = root.findtext("nombre", "Desconocida")
        provincia = root.findtext("provincia", "")
        localidad = f"{nombre} ({provincia})" if provincia else nombre

        dias = []
        prediccion = root.find("prediccion")
        if prediccion is None:
            return {"localidad": localidad, "url": url, "dias": []}

        for dia in prediccion.findall("dia"):
            fecha = dia.get("fecha", "")

            temp = dia.find("temperatura")
            tmax = temp.findtext("maxima", "N/A") if temp is not None else "N/A"
            tmin = temp.findtext("minima", "N/A") if temp is not None else "N/A"

            estado = "Desconocido"
            for ec in dia.findall("estado_cielo"):
                desc = ec.get("descripcion")
                if desc:
                    estado = desc
                    break

            prob_pp = "N/A"
            for pp in dia.findall("prob_precipitacion"):
                periodo = pp.get("periodo")
                if periodo in ["00-24", "00-00", None] and pp.text:
                    prob_pp = f"{pp.text}%"
                    break
            if prob_pp == "N/A":
                # fallback: get first period with value
                for pp in dia.findall("prob_precipitacion"):
                    if pp.text and pp.text.strip():
                        prob_pp = f"{pp.text}%"
                        break

            # Wind: find 00-24 period or highest speed
            viento_str = ""
            viento_max_vel = -1
            for v in dia.findall("viento"):
                periodo = v.get("periodo")
                v_dir = v.findtext("direccion", "")
                v_vel_txt = v.findtext("velocidad", "")
                try:
                    v_vel = int(v_vel_txt)
                except (ValueError, TypeError):
                    v_vel = 0
                if periodo in ["00-24", "00-00"]:
                    viento_str = f"{v_dir} {v_vel}".strip() if v_dir else str(v_vel)
                    break
                if v_vel > viento_max_vel:
                    viento_max_vel = v_vel
                    viento_str = f"{v_dir} {v_vel}".strip() if v_dir else str(v_vel)
            if not viento_str:
                viento_str = "N/A"

            dias.append({
                "fecha": fecha,
                "temp": f"{tmin}°/{tmax}°C",
                "estado": estado,
                "prob_precip": prob_pp,
                "viento": viento_str,
                "uv": dia.findtext("uv_max", "N/A"),
            })

        return {"localidad": localidad, "url": url, "dias": dias}

    except ET.ParseError:
        return {"localidad": f"Error ({url})", "url": url, "dias": []}


def format_date(fecha_iso: str) -> str:
    """Convert ISO date string to Spanish human-readable format.

    Transforms dates like "2024-01-15" to "Lunes 15 de ene".

    Args:
        fecha_iso: Date string in ISO format (YYYY-MM-DD).

    Returns:
        Human-readable date string in Spanish, or original if parsing fails.
    """
    try:
        dt = datetime.fromisoformat(fecha_iso)
        dias = [
            "Lunes", "Martes", "Miércoles", "Jueves",
            "Viernes", "Sábado", "Domingo",
        ]
        meses = [
            "ene", "feb", "mar", "abr", "may", "jun",
            "jul", "ago", "sep", "oct", "nov", "dic",
        ]
        return f"{dias[dt.weekday()]} {dt.day} de {meses[dt.month - 1]}"
    except Exception:
        return fecha_iso


def load_urls(filepath: str) -> list[str]:
    """Load AEMET URLs from a text file.

    Reads the file, skipping blank lines and comments (lines starting with #).
    Supports inline comments after URLs (separated by #).

    Args:
        filepath: Path to the urls.txt file.

    Returns:
        List of URL strings, excluding comments and blank lines.
    """
    urls = []
    with open(filepath, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            url = line.split("#")[0].strip()
            if url:
                urls.append(url)
    return urls


def build_comparative_forecast(locations_data: list[dict], max_days: int = 3) -> str:
    """Build a comparative forecast string from multiple locations.

    Organizes weather data by date, showing all locations side by side
    for each day. Only includes dates from today forward.

    Args:
        locations_data: List of location dictionaries from parse_location_xml.
        max_days: Maximum number of days to include (default 3).

    Returns:
        Formatted markdown string with comparative forecast, or error message
        if no valid data is available.
    """
    if not locations_data:
        return "❌ *No hay datos válidos para mostrar.*"

    by_date: dict[str, list[dict]] = {}
    for loc in locations_data:
        for dia in loc["dias"][:max_days]:
            by_date[dia["fecha"]].append({
                "localidad": loc["localidad"],
                "estado": dia["estado"],
                "temp": dia["temp"],
                "prob_precip": dia["prob_precip"],
                "viento": dia["viento"],
                "uv": dia["uv"],
            })

    sorted_dates = sorted(
        d for d in by_date.keys() if d >= date.today().isoformat()
    )

    lines = []
    lines.append("🌤️ *RESUMEN METEOROLÓGICO AEMET* 🌤️\n")

    for fecha in sorted_dates:
        dias_fmt = format_date(fecha)
        lines.append(f"📅 *{dias_fmt.upper()}*")
        lines.append("▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀")

        for entry in by_date[fecha]:
            lines.append(f"📍 *{entry['localidad']}*")
            lines.append(f" └ ☁️ {entry['estado']} | 🌡️ {entry['temp']}")
            lines.append(f" └ 🌧️ Lluvia: {entry['prob_precip']} | 💨 Viento: {entry['viento']} km/h")
            if entry["uv"] != "N/A":
                lines.append(f" └ ☀️ UV máx: {entry['uv']}")
            lines.append("")

    lines.append("🔗 _Fuente: [AEMET](https://www.aemet.es)_")

    return "\n".join(lines)


def main() -> None:
    """Main entry point for the weather tool.

    Reads request from stdin, fetches weather data for configured locations,
    and writes the comparative forecast to stdout as JSON.
    """
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})
        context = request.get("context", {})

        urls_input = arguments.get("urls")
        max_days = arguments.get("max_days", 3)

        # Validate and clamp max_days to 1-7 range
        if not isinstance(max_days, int) or max_days < 1:
            max_days = 3
        if max_days > 7:
            max_days = 7

        # Determine URL list: user-provided, file-based, or error
        if urls_input and isinstance(urls_input, list):
            urls = urls_input
        elif os.path.exists(DEFAULT_URLS_FILE):
            urls = load_urls(DEFAULT_URLS_FILE)
        else:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "NO_URLS",
                    "message": "No URLs provided and no urls.txt found in tool directory",
                },
            })
            return

        if not urls:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {"code": "NO_URLS", "message": "No valid URLs provided"},
            })
            return

        # Fetch and parse weather data for all locations
        locations_data = []
        for url in urls:
            xml = fetch_xml(url)
            if xml:
                data = parse_location_xml(xml, url)
                if data["dias"]:
                    locations_data.append(data)

        output = build_comparative_forecast(locations_data, max_days)

        structured = {
            "locations": [loc["localidad"] for loc in locations_data],
            "days_shown": max_days,
            "source": "AEMET",
            "urls_used": urls,
        }

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": output}],
            "structured_content": structured,
        })

    except json.JSONDecodeError:
        write_response({
            "success": False,
            "request_id": "",
            "error": {
                "code": "INVALID_JSON",
                "message": "Failed to parse JSON request",
            },
        })
    except Exception as e:
        write_response({
            "success": False,
            "request_id": request_id if "request_id" in locals() else "",
            "error": {"code": "EXECUTION_FAILED", "message": str(e)},
        })


if __name__ == "__main__":
    main()
