"""
Data File Injector Filter for OpenWebUI.

Intercepts chat requests that contain Excel/CSV file attachments,
generates a time-limited S3 presigned URL for each data file,
injects the URL into the user message as tool context, and removes
the file from the RAG pipeline (preventing OpenWebUI from doing text
extraction on binary files).

Installation:
  OpenWebUI Admin Panel → Functions → + New Function → paste this code.
  Configure Valves with your RustFS/S3 credentials.

Requirements:
  - OpenWebUI configured with STORAGE_PROVIDER=s3 pointing to RustFS
  - boto3 (already available in OpenWebUI container when S3 is configured)
"""

import re
from pathlib import Path
from typing import Optional
from pydantic import BaseModel


class Filter:
    class Valves(BaseModel):
        S3_ENDPOINT_URL: str = "http://rustfs:9000"
        S3_ACCESS_KEY_ID: str = ""
        S3_SECRET_ACCESS_KEY: str = ""
        S3_BUCKET_NAME: str = "openwebui"
        S3_REGION: str = "us-east-1"
        PRESIGNED_URL_EXPIRY_SECONDS: int = 900  # 15 minutes
        ENABLED: bool = True

    _SUPPORTED_EXTENSIONS = {".csv", ".xlsx", ".xls"}
    _UUID_PREFIX_RE = re.compile(
        r'^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}_',
        re.IGNORECASE
    )

    def __init__(self):
        self.valves = self.Valves()

    # ------------------------------------------------------------------
    # Private helpers
    # ------------------------------------------------------------------

    def _get_s3_client(self):
        import boto3
        from botocore.config import Config

        config = Config(
            s3={"addressing_style": "path"},
            request_checksum_calculation="when_required",
            response_checksum_validation="when_required",
        )
        return boto3.client(
            "s3",
            endpoint_url=self.valves.S3_ENDPOINT_URL,
            aws_access_key_id=self.valves.S3_ACCESS_KEY_ID,
            aws_secret_access_key=self.valves.S3_SECRET_ACCESS_KEY,
            region_name=self.valves.S3_REGION,
            config=config,
        )

    def _extract_s3_key(self, file_path: str) -> Optional[str]:
        """
        Extract S3 key from a file path stored in OpenWebUI DB.
        OpenWebUI stores S3 paths as: s3://{bucket}/{key}
        Returns None if not an S3 path.
        """
        if not file_path or not file_path.startswith("s3://"):
            return None
        # "s3://openwebui/uuid_filename.xlsx" → "uuid_filename.xlsx"
        after_scheme = file_path[5:]  # strip "s3://"
        parts = after_scheme.split("/", 1)
        if len(parts) < 2:
            return None
        return parts[1]  # everything after the bucket name

    def _get_original_filename(self, s3_key: str) -> str:
        """Strip UUID prefix from S3 key basename to get the original filename."""
        basename = Path(s3_key).name
        stripped = self._UUID_PREFIX_RE.sub("", basename)
        return stripped if stripped else basename

    def _is_data_file(self, name: str) -> bool:
        return Path(name).suffix.lower() in self._SUPPORTED_EXTENSIONS

    def _inject_tool_context(self, body: dict, injections: list[dict]) -> dict:
        """
        Append tool context lines to the last user message in body["messages"].
        injections: list of {"name": str, "url": str}
        """
        lines = ["\n\n---\n**[Data files attached — use the analyze_data tool]**"]
        for inj in injections:
            lines.append(f"- File: `{inj['name']}`")
            lines.append(f"  file_url: `{inj['url']}`")
            lines.append(f"  file_name: `{inj['name']}`")
        lines.append(
            "\nCall `analyze_data` with `file_url` and `file_name` from above, "
            "plus `question` matching the user's request."
        )
        tool_context = "\n".join(lines)

        messages = body.get("messages", [])
        for i in range(len(messages) - 1, -1, -1):
            if messages[i].get("role") == "user":
                content = messages[i].get("content", "")
                if isinstance(content, str):
                    messages[i]["content"] = content + tool_context
                elif isinstance(content, list):
                    messages[i]["content"].append(
                        {"type": "text", "text": tool_context}
                    )
                break
        body["messages"] = messages
        return body

    # ------------------------------------------------------------------
    # Filter hook
    # ------------------------------------------------------------------

    def inlet(self, body: dict, __user__: dict = {}) -> dict:
        """
        Called before every LLM request.
        Detects Excel/CSV attachments, generates presigned URLs, injects
        them into the user message, and strips the files from RAG processing.
        """
        if not self.valves.ENABLED:
            return body

        files = body.get("files", [])
        if not files:
            return body

        # Identify data files in the attachment list
        data_file_ids = set()
        for f in files:
            if f.get("type", "file") != "file":
                continue
            name = f.get("name", "") or (f.get("meta") or {}).get("name", "")
            if self._is_data_file(name):
                file_id = f.get("id", "")
                if file_id:
                    data_file_ids.add(file_id)

        if not data_file_ids:
            return body

        # Load FileModel from OpenWebUI DB to get S3 path
        try:
            from open_webui.models.files import Files
        except ImportError:
            # OpenWebUI internal import not available — fail gracefully
            return body

        injections = []
        files_to_keep = []

        for f in files:
            file_id = f.get("id", "")
            if file_id not in data_file_ids:
                files_to_keep.append(f)
                continue

            try:
                file_model = Files.get_file_by_id(file_id)
                if not file_model or not file_model.path:
                    # No path stored — leave in RAG pipeline as fallback
                    files_to_keep.append(f)
                    continue

                s3_key = self._extract_s3_key(file_model.path)
                if not s3_key:
                    # Local storage path (not S3) — leave in RAG pipeline as fallback
                    files_to_keep.append(f)
                    continue

                original_name = (
                    self._get_original_filename(s3_key)
                    or f.get("name", "")
                    or (f.get("meta") or {}).get("name", "file")
                )

                s3 = self._get_s3_client()
                presigned_url = s3.generate_presigned_url(
                    "get_object",
                    Params={
                        "Bucket": self.valves.S3_BUCKET_NAME,
                        "Key": s3_key,
                    },
                    ExpiresIn=self.valves.PRESIGNED_URL_EXPIRY_SECONDS,
                )

                injections.append({"name": original_name, "url": presigned_url})
                # Do NOT append to files_to_keep → file is removed from RAG pipeline

            except Exception:
                # Any error → fail gracefully, leave file in RAG pipeline
                files_to_keep.append(f)

        if injections:
            body["files"] = files_to_keep
            body = self._inject_tool_context(body, injections)

        return body
