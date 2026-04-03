#!/usr/bin/env python3
"""
Weather Forecast Tool — Open-Meteo backend.

Fetches daily forecasts for configured locations using the Open-Meteo API
(https://open-meteo.com). No API key required. Free for non-commercial use.

Default locations:
  - Valle de Manzanedo (Burgos): 42.8833, -3.5167
  - Puentes Viejas (Madrid):     41.0167, -3.6167
  - Madrid:                      40.4168, -3.7038
"""

import json
import os
import sys
from datetime import date, datetime
from pathlib import Path
from typing import Any

try:
    import urllib.request
    import urllib.parse
    URLLIB_AVAILABLE = True
except ImportError:
    URLLIB_AVAILABLE = False

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
try:
    from common.validators import is_internal_url
except ImportError:
    def is_internal_url(url: str) -> bool:  # type: ignore[misc]
        return False

# ---------------------------------------------------------------------------
# Default locations: (name, latitude, longitude)
# ---------------------------------------------------------------------------
DEFAULT_LOCATIONS = [
    ("Valle de Manzanedo (Burgos)", 42.8833, -3.5167),
    ("Puentes Viejas (Madrid)",     41.0167, -3.6167),
    ("Madrid",                      40.4168, -3.7038),
]

# Open-Meteo WMO weather code → Spanish description
WMO_CODES: dict[int, str] = {
    0:  "Despejado",
    1:  "Principalmente despejado",
    2:  "Parcialmente nuboso",
    3:  "Cubierto",
    45: "Niebla",
    48: "Niebla con escarcha",
    51: "Llovizna ligera",
    53: "Llovizna moderada",
    55: "Llovizna densa",
    61: "Lluvia ligera",
    63: "Lluvia moderada",
    65: "Lluvia intensa",
    71: "Nevada ligera",
    73: "Nevada moderada",
    75: "Nevada intensa",
    77: "Granizo",
    80: "Chubascos ligeros",
    81: "Chubascos moderados",
    82: "Chubascos violentos",
    85: "Chubascos de nieve",
    86: "Chubascos de nieve fuertes",
    95: "Tormenta",
    96: "Tormenta con granizo",
    99: "Tormenta con granizo intenso",
}

WMO_EMOJI: dict[int, str] = {
    0:  "☀️",
    1:  "🌤️",
    2:  "⛅",
    3:  "☁️",
    45: "🌫️",
    48: "🌫️",
    51: "🌦️",
    53: "🌦️",
    55: "🌧️",
    61: "🌧️",
    63: "🌧️",
    65: "🌧️",
    71: "🌨️",
    73: "🌨️",
    75: "❄️",
    77: "🌨️",
    80: "🌦️",
    81: "🌧️",
    82: "⛈️",
    85: "🌨️",
    86: "❄️",
    95: "⛈️",
    96: "⛈️",
    99: "⛈️",
}


def wmo_desc(code: int) -> str:
    return WMO_CODES.get(code, f"Código {code}")


def wmo_emoji(code: int) -> str:
    return WMO_EMOJI.get(code, "🌡️")


def format_date_es(iso_date: str) -> str:
    """Convert ISO date to Spanish human-readable format."""
    DAYS_ES = ["Lunes", "Martes", "Miércoles", "Jueves", "Viernes", "Sábado", "Domingo"]
    MONTHS_ES = ["ene", "feb", "mar", "abr", "may", "jun",
                 "jul", "ago", "sep", "oct", "nov", "dic"]
    try:
        dt = date.fromisoformat(iso_date)
        day_name = DAYS_ES[dt.weekday()]
        return f"{day_name} {dt.day} de {MONTHS_ES[dt.month - 1]}"
    except Exception:
        return iso_date


def fetch_forecast(lat: float, lon: float, max_days: int) -> dict[str, Any] | None:
    """Fetch forecast from Open-Meteo API."""
    params = {
        "latitude": lat,
        "longitude": lon,
        "daily": ",".join([
            "weathercode",
            "temperature_2m_max",
            "temperature_2m_min",
            "precipitation_probability_max",
            "windspeed_10m_max",
            "winddirection_10m_dominant",
            "uv_index_max",
        ]),
        "timezone": "Europe/Madrid",
        "forecast_days": max_days,
    }
    url = "https://api.open-meteo.com/v1/forecast?" + urllib.parse.urlencode(params)

    # SSRF protection: block internal/private URLs
    if is_internal_url(url):
        return None

    try:
        req = urllib.request.Request(url, headers={"User-Agent": "mcp-weather/2.0"})
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read().decode())
    except Exception as e:
        return None


def wind_direction_es(degrees: float | None) -> str:
    """Convert wind degrees to Spanish compass direction."""
    if degrees is None:
        return "N/D"
    directions = ["N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE",
                  "S", "SSO", "SO", "OSO", "O", "ONO", "NO", "NNO"]
    idx = round(degrees / 22.5) % 16
    return directions[idx]


def build_forecast_text(locations: list[tuple[str, float, float]], max_days: int) -> str:
    """Build comparative forecast markdown for all locations."""
    # Fetch all locations
    all_data: list[tuple[str, dict]] = []
    for name, lat, lon in locations:
        data = fetch_forecast(lat, lon, max_days)
        if data and "daily" in data:
            all_data.append((name, data["daily"]))

    if not all_data:
        return "❌ *No se pudo obtener el pronóstico. Comprueba la conexión.*"

    # Get date list from first location
    dates = all_data[0][1].get("time", [])
    today = date.today().isoformat()
    future_dates = [d for d in dates if d >= today][:max_days]

    lines = ["🌤️ *PRONÓSTICO METEOROLÓGICO* 🌤️\n"]

    for fecha in future_dates:
        idx = dates.index(fecha)
        fecha_fmt = format_date_es(fecha).upper()
        lines.append(f"📅 *{fecha_fmt}*")
        lines.append("▀" * 25)

        for name, daily in all_data:
            try:
                code = int(daily.get("weathercode", [0])[idx] or 0)
                tmax = daily.get("temperature_2m_max", [None])[idx]
                tmin = daily.get("temperature_2m_min", [None])[idx]
                precip = daily.get("precipitation_probability_max", [None])[idx]
                wind_spd = daily.get("windspeed_10m_max", [None])[idx]
                wind_dir = daily.get("winddirection_10m_dominant", [None])[idx]
                uv = daily.get("uv_index_max", [None])[idx]

                estado = f"{wmo_emoji(code)} {wmo_desc(code)}"
                temp = f"{int(tmin)}°/{int(tmax)}°C" if tmin is not None and tmax is not None else "N/D"
                lluvia = f"{int(precip)}%" if precip is not None else "N/D"
                viento = f"{wind_direction_es(wind_dir)} {int(wind_spd)} km/h" if wind_spd is not None else "N/D"
                uv_str = f"{int(uv)}" if uv is not None else "N/D"

                lines.append(f"📍 *{name}*")
                lines.append(f" └ {estado} | 🌡️ {temp}")
                lines.append(f" └ 🌧️ Lluvia: {lluvia} | 💨 {viento}")
                lines.append(f" └ ☀️ UV máx: {uv_str}")
            except (IndexError, TypeError, ValueError):
                lines.append(f"📍 *{name}* — sin datos")

        lines.append("")

    lines.append("_Fuente: Open-Meteo (ECMWF/DWD)_")
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# MCP stdio protocol
# ---------------------------------------------------------------------------

def read_request() -> dict:
    raw = sys.stdin.read()
    return json.loads(raw)


def write_response(data: dict) -> None:
    print(json.dumps(data, ensure_ascii=False), flush=True)


def main() -> None:
    request_id = ""
    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        max_days = arguments.get("max_days", 3)
        if not isinstance(max_days, int) or max_days < 1:
            max_days = 3
        if max_days > 7:
            max_days = 7

        # Support custom locations via arguments
        custom_locs = arguments.get("locations")
        if custom_locs and isinstance(custom_locs, list):
            locations = []
            for loc in custom_locs:
                if "name" not in loc or "lat" not in loc or "lon" not in loc:
                    continue
                lat = float(loc["lat"])
                lon = float(loc["lon"])
                # Validate geographic bounds
                if not (-90 <= lat <= 90) or not (-180 <= lon <= 180):
                    continue
                locations.append((str(loc["name"])[:64], lat, lon))
            if not locations:
                locations = DEFAULT_LOCATIONS
        else:
            locations = DEFAULT_LOCATIONS

        output = build_forecast_text(locations, max_days)

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [{"type": "text", "text": output}],
            "structured_content": {
                "source": "Open-Meteo",
                "locations": [loc[0] for loc in locations],
                "days_shown": max_days,
            },
        })

    except json.JSONDecodeError:
        write_response({
            "success": False,
            "request_id": "",
            "error": {"code": "INVALID_JSON", "message": "Failed to parse JSON request"},
        })
    except Exception as e:
        write_response({
            "success": False,
            "request_id": request_id if "request_id" in locals() else "",
            "error": {"code": "EXECUTION_FAILED", "message": str(e)},
        })


if __name__ == "__main__":
    main()
