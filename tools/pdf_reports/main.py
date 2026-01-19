#!/usr/bin/env python3
"""
PDF Report Generation Tool.
Generates PDF reports from structured data using Jinja2 templates and WeasyPrint.
"""

import json
import os
import sys
from datetime import datetime
from pathlib import Path
from typing import Any

try:
    from jinja2 import Environment, FileSystemLoader, select_autoescape
    from weasyprint import HTML, CSS
    DEPENDENCIES_AVAILABLE = True
except ImportError:
    DEPENDENCIES_AVAILABLE = False


TEMPLATES_DIR = Path("/app/templates/reports")
DEFAULT_OUTPUT_DIR = Path("/data/reports")


def read_request() -> dict[str, Any]:
    """Read JSON request from STDIN."""
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to STDOUT."""
    print(json.dumps(response))


def get_template_env() -> Environment:
    """Create Jinja2 environment with template directory."""
    return Environment(
        loader=FileSystemLoader(TEMPLATES_DIR),
        autoescape=select_autoescape(['html', 'xml'])
    )


def generate_pdf(html_content: str, output_path: Path, css_path: Path | None = None) -> None:
    """Generate PDF from HTML content."""
    html = HTML(string=html_content)

    stylesheets = []
    if css_path and css_path.exists():
        stylesheets.append(CSS(filename=str(css_path)))

    # Ensure output directory exists
    output_path.parent.mkdir(parents=True, exist_ok=True)

    html.write_pdf(str(output_path), stylesheets=stylesheets)


def render_incident_report(data: dict[str, Any], env: Environment) -> str:
    """Render incident report template."""
    template = env.get_template("incident.html")

    # Add default values and formatting
    context = {
        "title": data.get("title", "Incident Report"),
        "incident_id": data.get("incident_id", "N/A"),
        "date": data.get("date", datetime.now().strftime("%Y-%m-%d")),
        "time": data.get("time", datetime.now().strftime("%H:%M")),
        "severity": data.get("severity", "Medium"),
        "status": data.get("status", "Open"),
        "reported_by": data.get("reported_by", "Unknown"),
        "description": data.get("description", ""),
        "affected_systems": data.get("affected_systems", []),
        "timeline": data.get("timeline", []),
        "root_cause": data.get("root_cause", "Under investigation"),
        "resolution": data.get("resolution", "Pending"),
        "lessons_learned": data.get("lessons_learned", []),
        "generated_at": datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    }

    return template.render(**context)


def render_meeting_report(data: dict[str, Any], env: Environment) -> str:
    """Render meeting minutes template."""
    template = env.get_template("meeting.html")

    context = {
        "title": data.get("title", "Meeting Minutes"),
        "meeting_id": data.get("meeting_id", "N/A"),
        "date": data.get("date", datetime.now().strftime("%Y-%m-%d")),
        "time": data.get("time", ""),
        "duration": data.get("duration", ""),
        "location": data.get("location", ""),
        "organizer": data.get("organizer", ""),
        "attendees": data.get("attendees", []),
        "absent": data.get("absent", []),
        "agenda": data.get("agenda", []),
        "discussions": data.get("discussions", []),
        "decisions": data.get("decisions", []),
        "action_items": data.get("action_items", []),
        "next_meeting": data.get("next_meeting", "TBD"),
        "generated_at": datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    }

    return template.render(**context)


def render_audit_report(data: dict[str, Any], env: Environment) -> str:
    """Render audit report template."""
    template = env.get_template("audit.html")

    context = {
        "title": data.get("title", "Audit Report"),
        "audit_id": data.get("audit_id", "N/A"),
        "audit_type": data.get("audit_type", "General"),
        "period_start": data.get("period_start", ""),
        "period_end": data.get("period_end", ""),
        "auditor": data.get("auditor", ""),
        "department": data.get("department", ""),
        "executive_summary": data.get("executive_summary", ""),
        "scope": data.get("scope", []),
        "methodology": data.get("methodology", ""),
        "findings": data.get("findings", []),
        "recommendations": data.get("recommendations", []),
        "management_response": data.get("management_response", ""),
        "risk_rating": data.get("risk_rating", "Medium"),
        "generated_at": datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    }

    return template.render(**context)


def main() -> None:
    if not DEPENDENCIES_AVAILABLE:
        write_response({
            "success": False,
            "request_id": "",
            "error": {
                "code": "DEPENDENCY_MISSING",
                "message": "Required dependencies not available. Install: jinja2, weasyprint"
            }
        })
        return

    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        report_type = arguments.get("report_type")
        data = arguments.get("data", {})
        output_path = arguments.get("output_path")

        if not report_type:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "INVALID_INPUT",
                    "message": "report_type is required"
                }
            })
            return

        # Generate output path if not provided
        if not output_path:
            timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
            output_path = DEFAULT_OUTPUT_DIR / f"{report_type}_{timestamp}.pdf"
        else:
            output_path = Path(output_path)

        # Get template environment
        env = get_template_env()

        # Render the appropriate template
        renderers = {
            "incident": render_incident_report,
            "meeting": render_meeting_report,
            "audit": render_audit_report
        }

        if report_type not in renderers:
            write_response({
                "success": False,
                "request_id": request_id,
                "error": {
                    "code": "INVALID_INPUT",
                    "message": f"Unknown report type: {report_type}. Valid types: {list(renderers.keys())}"
                }
            })
            return

        html_content = renderers[report_type](data, env)

        # Get CSS file if exists
        css_path = TEMPLATES_DIR / "styles.css"

        # Generate PDF
        generate_pdf(html_content, output_path, css_path if css_path.exists() else None)

        write_response({
            "success": True,
            "request_id": request_id,
            "content": [
                {
                    "type": "text",
                    "text": f"Report generated successfully: {output_path}"
                }
            ],
            "structured_content": {
                "report_type": report_type,
                "output_path": str(output_path),
                "file_size": output_path.stat().st_size if output_path.exists() else 0
            }
        })

    except FileNotFoundError as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", "") if 'request' in dir() else "",
            "error": {
                "code": "FILE_NOT_FOUND",
                "message": str(e)
            }
        })
    except Exception as e:
        write_response({
            "success": False,
            "request_id": request.get("request_id", "") if 'request' in dir() else "",
            "error": {
                "code": "EXECUTION_FAILED",
                "message": str(e),
                "details": str(type(e).__name__)
            }
        })


if __name__ == "__main__":
    main()
