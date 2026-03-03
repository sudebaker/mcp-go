#!/usr/bin/env python3
"""
PDF Report Generation Tool.
Generates PDF reports from structured data using Jinja2 templates and WeasyPrint.

Supported report types:
- incident: Incident reports
- meeting: Meeting minutes
- audit: Audit reports
- executive_summary: Executive summary reports with key findings
- formal_report: Formal reports with charts and tables
- corporate_email: Corporate email format (plain text style)
"""

import json
import os
import sys
import traceback
import base64
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from common.validators import validate_output_path
from common.structured_logging import get_logger

logger = get_logger(__name__, "pdf_reports")

try:
    from jinja2 import Environment, FileSystemLoader, select_autoescape
    from jinja2.sandbox import SandboxedEnvironment
    from weasyprint import HTML, CSS
    import markdown
    from minio import Minio
    from minio.error import S3Error

    DEPENDENCIES_AVAILABLE = True
except ImportError:
    DEPENDENCIES_AVAILABLE = False
    S3Error = Exception
    SandboxedEnvironment = None


_template_env = None


def get_template_env() -> Environment:
    """Get or create cached Jinja2 environment with sandbox security.
    
    SECURITY: Uses SandboxedEnvironment to prevent template injection attacks
    while allowing safe template operations.
    """
    global _template_env
    if _template_env is None:
        templates_dir = Path(os.environ.get("TEMPLATES_DIR", "/app/templates/reports"))
        
        # Use SandboxedEnvironment for security (prevents SSTI)
        if SandboxedEnvironment is not None:
            _template_env = SandboxedEnvironment(
                loader=FileSystemLoader(templates_dir),
                autoescape=select_autoescape(["html", "xml"]),
            )
        else:
            # Fallback if sandbox not available (shouldn't happen in normal setup)
            _template_env = Environment(
                loader=FileSystemLoader(templates_dir),
                autoescape=select_autoescape(["html", "xml"]),
            )
    return _template_env


def get_default_output_dir() -> Path:
    """Get default output directory."""
    return Path(os.environ.get("OUTPUT_DIR", "/data/reports"))


def read_request() -> dict[str, Any]:
    """Read JSON request from STDIN."""
    input_data = sys.stdin.read()
    return json.loads(input_data)


def write_response(response: dict[str, Any]) -> None:
    """Write JSON response to STDOUT."""
    print(json.dumps(response))


def build_base_context(data: dict[str, Any], report_type: str) -> dict[str, Any]:
    """Build common context with defaults."""
    return {
        "report_type": report_type,
        "generated_at": datetime.now().strftime("%Y-%m-%d %H:%M:%S"),
        "title": data.get("title", f"{report_type.title()} Report"),
    }


def generate_pdf(
    html_content: str, output_path: Path, css_path: Path | None = None
) -> None:
    """Generate PDF from HTML content."""
    html = HTML(string=html_content)

    stylesheets = []
    if css_path and css_path.exists():
        stylesheets.append(CSS(filename=str(css_path)))

    # Ensure output directory exists
    output_path.parent.mkdir(parents=True, exist_ok=True)

    html.write_pdf(str(output_path), stylesheets=stylesheets)


def get_rustfs_client() -> Minio | None:
    """Get MinIO client configured for RustFS."""
    endpoint = os.environ.get("RUSTFS_ENDPOINT", "rustfs:9000")
    access_key = os.environ.get("RUSTFS_ACCESS_KEY_ID", "rustfsadmin")
    secret_key = os.environ.get("RUSTFS_SECRET_ACCESS_KEY", "rustfsadmin")
    use_ssl = os.environ.get("RUSTFS_USE_SSL", "false").lower() == "true"

    if not endpoint:
        return None

    try:
        client = Minio(
            endpoint,
            access_key=access_key,
            secret_key=secret_key,
            secure=use_ssl,
        )
        return client
    except Exception:
        return None


def upload_to_rustfs(file_path: Path, bucket: str = "reports") -> dict | None:
    """Upload PDF to RustFS and return presigned URL and file info."""
    client = get_rustfs_client()
    if not client:
        logger.warning("RustFS client not available, skipping upload")
        return None

    try:
        # Ensure bucket exists
        if not client.bucket_exists(bucket):
            client.make_bucket(bucket)

        # Generate unique object name
        timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
        object_name = f"reports/{timestamp}_{file_path.name}"

        # Upload file
        client.fput_object(
            bucket,
            object_name,
            str(file_path),
            content_type="application/pdf",
        )

        # Generate presigned URL (expires in 1 hour)
        presigned_url = client.presigned_get_object(bucket, object_name, expires=timedelta(hours=1))

        return {
            "bucket": bucket,
            "object_name": object_name,
            "presigned_url": presigned_url,
        }
    except S3Error as e:
        logger.error(f"Failed to upload to RustFS: {e}")
        return None
    except Exception as e:
        logger.error(f"Unexpected error uploading to RustFS: {e}")
        return None


def render_incident_report(data: dict[str, Any], env: Environment) -> str:
    """Render incident report template."""
    template = env.get_template("incident.html")

    context = build_base_context(data, "incident")
    context.update(
        {
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
        }
    )

    return template.render(**context)


def render_meeting_report(data: dict[str, Any], env: Environment) -> str:
    """Render meeting minutes template."""
    template = env.get_template("meeting.html")

    context = build_base_context(data, "meeting")
    context.update(
        {
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
        }
    )

    return template.render(**context)


def render_audit_report(data: dict[str, Any], env: Environment) -> str:
    """Render audit report template."""
    template = env.get_template("audit.html")

    context = build_base_context(data, "audit")
    context.update(
        {
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
        }
    )

    return template.render(**context)


def render_executive_summary_report(data: dict[str, Any], env: Environment) -> str:
    """Render executive summary report template."""
    template = env.get_template("executive_summary.html")

    context = build_base_context(data, "executive_summary")
    context.update(
        {
            "logo_url": data.get("logo_url"),
            "prepared_by": data.get("prepared_by", ""),
            "reviewed_by": data.get("reviewed_by", ""),
            "period": data.get("period", ""),
            "date": data.get("date", datetime.now().strftime("%Y-%m-%d")),
            "executive_summary": data.get("executive_summary", ""),
            "key_findings": data.get("key_findings", []),
            "recommendations": data.get("recommendations", []),
            "next_steps": data.get("next_steps", []),
            "additional_notes": data.get("additional_notes", ""),
        }
    )

    return template.render(**context)


def render_formal_report(data: dict[str, Any], env: Environment) -> str:
    """Render formal report with charts template."""
    template = env.get_template("formal_report.html")

    context = build_base_context(data, "formal_report")
    context.update(
        {
            "logo_url": data.get("logo_url"),
            "report_id": data.get("report_id", ""),
            "author": data.get("author", ""),
            "department": data.get("department", ""),
            "period_start": data.get("period_start", ""),
            "period_end": data.get("period_end", ""),
            "date": data.get("date", datetime.now().strftime("%Y-%m-%d")),
            "classification": data.get("classification", ""),
            "executive_summary": data.get("executive_summary", ""),
            "sections": data.get("sections", []),
            "recommendations": data.get("recommendations", []),
            "conclusion": data.get("conclusion", ""),
            "appendix": data.get("appendix", []),
            "confidentiality": data.get("confidentiality", "Confidential Document"),
        }
    )

    return template.render(**context)


def render_corporate_email(data: dict[str, Any], env: Environment) -> str:
    """Render corporate email template."""
    template = env.get_template("corporate_email.html")

    context = build_base_context(data, "corporate_email")
    context.update(
        {
            "logo_url": data.get("logo_url"),
            "from": data.get("from", ""),
            "to": data.get("to", ""),
            "cc": data.get("cc", ""),
            "bcc": data.get("bcc", ""),
            "subject": data.get("subject", "Email Communication"),
            "date": data.get("date", datetime.now().strftime("%Y-%m-%d")),
            "salutation": data.get("salutation", ""),
            "body": data.get("body", ""),
            "body_sections": data.get("body_sections", []),
            "action_required": data.get("action_required", ""),
            "attachments": data.get("attachments", []),
            "closing": data.get("closing", ""),
            "signature": data.get("signature", {}),
            "confidentiality": data.get(
                "confidentiality", "Confidential Email Communication"
            ),
        }
    )

    return template.render(**context)


def render_llm_response(data: dict[str, Any], env: Environment) -> str:
    """Render LLM response as PDF report with corporate styling."""
    template = env.get_template("llm_response.html")

    content_markdown = data.get("content", "")
    content_html = markdown.markdown(
        content_markdown,
        extensions=["tables", "fenced_code", "nl2br"]
    )

    context = build_base_context(data, "llm_response")
    context.update({
        "content_html": content_html,
        "author": data.get("author", "AI Assistant"),
        "logo_url": data.get("logo_url"),
        "confidentiality": data.get("confidentiality", "Internal Document"),
    })

    return template.render(**context)


def main() -> None:
    if not DEPENDENCIES_AVAILABLE:
        write_response(
            {
                "success": False,
                "request_id": "",
                "error": {
                    "code": "DEPENDENCY_MISSING",
                    "message": "Required dependencies not available. Install: jinja2, weasyprint",
                },
            }
        )
        return

    try:
        request = read_request()
        request_id = request.get("request_id", "")
        arguments = request.get("arguments", {})

        report_type = arguments.get("report_type")
        data = arguments.get("data", {})
        output_path = arguments.get("output_path")

        if not report_type:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_INPUT",
                        "message": "report_type is required",
                    },
                }
            )
            return

        if not output_path:
            timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
            output_path = get_default_output_dir() / f"{report_type}_{timestamp}.pdf"
        else:
            output_path_str = output_path
            validate_output_path(output_path_str, str(get_default_output_dir()))
            output_path = Path(output_path_str)

        env = get_template_env()

        renderers = {
            "incident": render_incident_report,
            "meeting": render_meeting_report,
            "audit": render_audit_report,
            "executive_summary": render_executive_summary_report,
            "formal_report": render_formal_report,
            "corporate_email": render_corporate_email,
            "llm_response": render_llm_response,
        }

        if report_type not in renderers:
            write_response(
                {
                    "success": False,
                    "request_id": request_id,
                    "error": {
                        "code": "INVALID_INPUT",
                        "message": f"Unknown report type: {report_type}. Valid types: {list(renderers.keys())}",
                    },
                }
            )
            return

        html_content = renderers[report_type](data, env)

        templates_dir = Path(os.environ.get("TEMPLATES_DIR", "/app/templates/reports"))
        styles_css_path = templates_dir / "styles.css"

        generate_pdf(
            html_content,
            output_path,
            styles_css_path if styles_css_path.exists() else None,
        )

        # Read the generated PDF file and encode as base64
        with open(output_path, "rb") as pdf_file:
            pdf_content = pdf_file.read()
            pdf_base64 = base64.b64encode(pdf_content).decode("utf-8")

        # Upload to RustFS
        rustfs_info = upload_to_rustfs(output_path)

        # Build response
        response_content = [
            {
                "type": "text",
                "text": f"Report generated successfully: {output_path}",
            },
            {
                "type": "resource",
                "resource": {
                    "uri": f"file://{output_path}",
                    "mimeType": "application/pdf",
                    "text": pdf_base64,
                },
            },
        ]

        if rustfs_info:
            response_content.append({
                "type": "text",
                "text": f"Report uploaded to storage: {rustfs_info['presigned_url']}",
            })

        structured_content = {
            "report_type": report_type,
            "output_path": str(output_path),
            "file_size": output_path.stat().st_size if output_path.exists() else 0,
            "pdf_base64": pdf_base64,
        }

        if rustfs_info:
            structured_content.update({
                "storage": {
                    "bucket": rustfs_info["bucket"],
                    "object_name": rustfs_info["object_name"],
                    "presigned_url": rustfs_info["presigned_url"],
                }
            })

        write_response({
            "success": True,
            "request_id": request_id,
            "content": response_content,
            "structured_content": structured_content,
        })

    except ValueError as e:
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", "")
                if "request" in dir()
                else "",
                "error": {"code": "INVALID_INPUT", "message": str(e)},
            }
        )
    except FileNotFoundError as e:
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", "")
                if "request" in dir()
                else "",
                "error": {"code": "FILE_NOT_FOUND", "message": str(e)},
            }
        )
    except Exception as e:
        write_response(
            {
                "success": False,
                "request_id": request.get("request_id", "")
                if "request" in dir()
                else "",
                "error": {
                    "code": "EXECUTION_FAILED",
                    "message": str(e),
                    "details": traceback.format_exc(),
                },
            }
        )


if __name__ == "__main__":
    main()
