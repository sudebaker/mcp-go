#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Script para obtener un resumen comparativo de la previsión meteorológica
de múltiples localidades desde ficheros XML de AEMET.

Formato de salida:
  📅 [Día]
     ├── 📍 [Localidad]
     │   ☁️ Estado | 🌡️ Temp | 🌧️ Prob. lluvia | 💨 Viento
"""

import xml.etree.ElementTree as ET
import urllib.request
import sys
import os
from datetime import datetime
from collections import defaultdict


def fetch_xml(url: str) -> str:
    """Descarga y decodifica el XML de AEMET (ISO-8859-15 → UTF-8)."""
    try:
        with urllib.request.urlopen(url, timeout=10) as response:
            return response.read().decode('iso-8859-15')
    except Exception as e:
        print(f"⚠️ Error con {url}: {e}", file=sys.stderr)
        return None


def parse_location_xml(xml_content: str, url: str) -> dict:
    """Extrae datos esenciales de un XML de localidad."""
    try:
        root = ET.fromstring(xml_content)
        nombre = root.findtext('nombre', 'Desconocida')
        provincia = root.findtext('provincia', '')
        localidad = f"{nombre} ({provincia})" if provincia else nombre

        dias = []
        prediccion = root.find('prediccion')
        if prediccion is None:
            return {'localidad': localidad, 'url': url, 'dias': []}

        for dia in prediccion.findall('dia'):
            fecha = dia.get('fecha', '')

            # Temperatura
            temp = dia.find('temperatura')
            tmax = temp.findtext('maxima', 'N/A') if temp is not None else 'N/A'
            tmin = temp.findtext('minima', 'N/A') if temp is not None else 'N/A'

            # Estado del cielo
            estado = 'Desconocido'
            for ec in dia.findall('estado_cielo'):
                desc = ec.get('descripcion')
                if desc:
                    estado = desc
                    break

            # Probabilidad precipitación (00-24h)
            prob_pp = 'N/A'
            for pp in dia.findall('prob_precipitacion'):
                if pp.get('periodo') in ['00-24', '00-00', None]:
                    prob_pp = f"{pp.text}%" if pp.text else 'N/A'
                    break

            # Viento
            viento = dia.find('viento')
            v_dir = viento.findtext('direccion', '') if viento is not None else ''
            v_vel = viento.findtext('velocidad', 'N/A') if viento is not None else 'N/A'
            viento_str = f"{v_dir} {v_vel}".strip() if v_dir else v_vel

            dias.append({
                'fecha': fecha,
                'temp': f"{tmin}°/{tmax}°C",
                'estado': estado,
                'prob_precip': prob_pp,
                'viento': viento_str,
                'uv': dia.findtext('uv_max', 'N/A')
            })

        return {'localidad': localidad, 'url': url, 'dias': dias}

    except ET.ParseError as e:
        print(f"⚠️ Error parseando XML de {url}: {e}", file=sys.stderr)
        return {'localidad': f"Error ({url})", 'url': url, 'dias': []}


def format_date(fecha_iso: str) -> str:
    """Convierte fecha ISO a formato legible: 'Lunes 2 de mar'."""
    try:
        dt = datetime.fromisoformat(fecha_iso)
        dias = ['Lunes', 'Martes', 'Miércoles', 'Jueves', 'Viernes', 'Sábado', 'Domingo']
        meses = ['ene', 'feb', 'mar', 'abr', 'may', 'jun',
                 'jul', 'ago', 'sep', 'oct', 'nov', 'dic']
        return f"{dias[dt.weekday()]} {dt.day} de {meses[dt.month-1]}"
    except:
        return fecha_iso


def load_urls(filepath: str) -> list:
    """Carga URLs desde fichero, ignorando comentarios y líneas vacías."""
    urls = []
    with open(filepath, 'r', encoding='utf-8') as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith('#'):
                continue
            # Separar URL de comentario inline
            url = line.split('#')[0].strip()
            if url:
                urls.append(url)
    return urls


def print_comparative_forecast(locations_data: list, max_days: int = 3):
    """Imprime el resumen comparativo: días → localidades → datos."""
    if not locations_data:
        print("❌ No hay datos válidos para mostrar.")
        return

    # Reorganizar: por fecha → lista de localidades
    by_date = defaultdict(list)
    for loc in locations_data:
        for dia in loc['dias'][:max_days]:
            by_date[dia['fecha']].append({
                'localidad': loc['localidad'],
                'estado': dia['estado'],
                'temp': dia['temp'],
                'prob_precip': dia['prob_precip'],
                'viento': dia['viento'],
                'uv': dia['uv']
            })

    # Ordenar fechas y mostrar
    sorted_dates = sorted(by_date.keys())

    print("\n" + "╔" + "═" * 78 + "╗")
    print("║" + "🌤️  RESUMEN COMPARATIVO - AEMET".center(78) + "║")
    print("╚" + "═" * 78 + "╝")

    for fecha in sorted_dates:
        dias_fmt = format_date(fecha)
        print(f"\n📅 {dias_fmt.upper()}")
        print("   " + "─" * 74)

        entries = by_date[fecha]
        for i, entry in enumerate(entries, 1):
            connector = "├──" if i < len(entries) else "└──"
            print(f"   {connector} 📍 {entry['localidad']}")
            print(f"   │   ☁️ {entry['estado']}")
            print(f"   │   🌡️ {entry['temp']}  🌧️ {entry['prob_precip']}  💨 {entry['viento']} km/h")
            if entry['uv'] != 'N/A':
                print(f"   │   ☀️ UV máx: {entry['uv']}")
            print("   │")

        print("   " + "─" * 74)

    print(f"\n📊 Fuente: AEMET | Localidades procesadas: {len(locations_data)}")
    print("🔗 https://www.aemet.es\n")


def main():
    urls_file = "urls.txt"
    max_days = 3  # Número de días a mostrar

    # Permitir pasar fichero de URLs como argumento
    if len(sys.argv) > 1:
        urls_file = sys.argv[1]
    if len(sys.argv) > 2:
        try:
            max_days = int(sys.argv[2])
        except ValueError:
            print("⚠️ Segundo argumento debe ser número de días (usando 3 por defecto)", file=sys.stderr)

    if not os.path.exists(urls_file):
        print(f"❌ Fichero no encontrado: {urls_file}", file=sys.stderr)
        print("💡 Crea un fichero 'urls.txt' con una URL por línea.")
        sys.exit(1)

    urls = load_urls(urls_file)
    if not urls:
        print("❌ No se encontraron URLs válidas en el fichero.", file=sys.stderr)
        sys.exit(1)

    print(f"📡 Procesando {len(urls)} localidades...")

    locations_data = []
    for url in urls:
        print(f"   🔹 {url.split('/')[-1]}")
        xml = fetch_xml(url)
        if xml:
            data = parse_location_xml(xml, url)
            if data['dias']:
                locations_data.append(data)

    print_comparative_forecast(locations_data, max_days)


if __name__ == "__main__":
    main()