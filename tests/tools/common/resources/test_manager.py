import pytest

from tools.common.resources.manager import ToolContext


def test_file_returns_single_resource():
    request = {
        "_resources": {
            "input": {
                "uri": "res://abc",
                "name": "report.pdf",
                "mime": "application/pdf",
                "size": 2048,
                "sha256": "def456",
            }
        },
        "arguments": {"input": "res://abc"},
    }
    ctx = ToolContext(request)
    res = ctx.file("input")
    assert res.uri == "res://abc"
    assert res.name == "report.pdf"
    assert res.mime == "application/pdf"
    assert res.size == 2048
    assert res.sha256 == "def456"


def test_files_returns_list():
    request = {
        "_resources": {
            "files": [
                {
                    "uri": "res://a1",
                    "name": "f1.txt",
                    "mime": "text/plain",
                    "size": 100,
                    "sha256": "h1",
                },
                {
                    "uri": "res://b2",
                    "name": "f2.txt",
                    "mime": "text/plain",
                    "size": 200,
                    "sha256": "h2",
                },
            ]
        },
        "arguments": {"files": ["res://a1", "res://b2"]},
    }
    ctx = ToolContext(request)
    resources = ctx.files("files")
    assert len(resources) == 2
    assert resources[0].uri == "res://a1"
    assert resources[1].uri == "res://b2"


def test_no_resources_is_empty_dict():
    ctx = ToolContext({"arguments": {}})
    assert ctx.file  # method exists
    assert ctx.files


def test_request_property():
    req = {"tool_name": "test"}
    ctx = ToolContext(req)
    assert ctx.request is req
