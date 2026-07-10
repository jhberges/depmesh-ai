"""Minimal MCP (Model Context Protocol) server over stdio.

Implements just enough of the protocol — initialize, tools/list,
tools/call, ping — as plain JSON-RPC 2.0, with no SDK dependency. This
keeps the whole project at zero runtime dependencies, which is fitting
for a dependency-vetting tool.

Wire this into a coding agent (e.g. Claude Code) so the agent can call
`vet_dependency` *before* installing a package it is about to add:

    {"mcpServers": {"depmesh": {"command": "python", "args": ["-m", "depmesh_ai", "serve"]}}}
"""
from __future__ import annotations

import json
import sys
from typing import Any

from .sources.http import SourceUnavailable
from .vet import vet

PROTOCOL_VERSION = "2025-06-18"

TOOLS = [
    {
        "name": "vet_dependency",
        "description": (
            "Vet an open-source package before adopting or installing it. "
            "Verifies the package actually exists on its registry (guards "
            "against hallucinated/slopsquatted names) and scores its health: "
            "release cadence, staleness, deprecation, maintainer bus-factor, "
            "license, and known advisories. Call this before adding any new "
            "dependency to a project. Returns ADOPT, CAUTION, or REJECT with "
            "per-signal reasons."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "ecosystem": {
                    "type": "string",
                    "enum": ["npm", "pypi", "maven"],
                    "description": "Package ecosystem/registry",
                },
                "package": {
                    "type": "string",
                    "description": (
                        "Package name. For maven use groupId:artifactId, "
                        "e.g. org.apache.commons:commons-lang3"
                    ),
                },
            },
            "required": ["ecosystem", "package"],
            "additionalProperties": False,
        },
    }
]


def _handle(request: dict) -> dict | None:
    method = request.get("method")
    request_id = request.get("id")
    params = request.get("params") or {}

    # Notifications (no id) get no response.
    if request_id is None:
        return None

    if method == "initialize":
        return _result(
            request_id,
            {
                "protocolVersion": params.get("protocolVersion", PROTOCOL_VERSION),
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "depmesh-ai", "version": "0.1.0"},
            },
        )
    if method == "ping":
        return _result(request_id, {})
    if method == "tools/list":
        return _result(request_id, {"tools": TOOLS})
    if method == "tools/call":
        return _call_tool(request_id, params)
    return _error(request_id, -32601, f"method not found: {method}")


def _call_tool(request_id: Any, params: dict) -> dict:
    if params.get("name") != "vet_dependency":
        return _error(request_id, -32602, f"unknown tool: {params.get('name')}")
    arguments = params.get("arguments") or {}
    try:
        verdict = vet(arguments["ecosystem"], arguments["package"])
        text = verdict.to_json()
        is_error = False
    except SourceUnavailable as err:
        text = (
            "Registry unreachable — could NOT vet this package. Do not treat "
            f"this as approval or as proof of non-existence. Detail: {err}"
        )
        is_error = True
    except (KeyError, ValueError) as err:
        text = f"Invalid arguments: {err}"
        is_error = True
    return _result(
        request_id,
        {"content": [{"type": "text", "text": text}], "isError": is_error},
    )


def _result(request_id: Any, result: dict) -> dict:
    return {"jsonrpc": "2.0", "id": request_id, "result": result}


def _error(request_id: Any, code: int, message: str) -> dict:
    return {"jsonrpc": "2.0", "id": request_id, "error": {"code": code, "message": message}}


def serve(stdin=None, stdout=None) -> None:
    stdin = stdin or sys.stdin
    stdout = stdout or sys.stdout
    for line in stdin:
        line = line.strip()
        if not line:
            continue
        try:
            request = json.loads(line)
        except json.JSONDecodeError:
            response = _error(None, -32700, "parse error")
        else:
            response = _handle(request)
        if response is not None:
            stdout.write(json.dumps(response) + "\n")
            stdout.flush()


if __name__ == "__main__":
    serve()
