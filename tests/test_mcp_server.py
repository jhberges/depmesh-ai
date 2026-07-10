import io
import json

from depmesh_ai import mcp_server


def rpc(*requests: dict) -> list[dict]:
    stdin = io.StringIO("\n".join(json.dumps(r) for r in requests) + "\n")
    stdout = io.StringIO()
    mcp_server.serve(stdin=stdin, stdout=stdout)
    return [json.loads(line) for line in stdout.getvalue().splitlines()]


def test_initialize_and_list_tools():
    responses = rpc(
        {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18"}},
        {"jsonrpc": "2.0", "method": "notifications/initialized"},
        {"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
    )
    assert len(responses) == 2  # notification gets no response
    init, tools = responses
    assert init["result"]["serverInfo"]["name"] == "depmesh-ai"
    assert tools["result"]["tools"][0]["name"] == "vet_dependency"


def test_unknown_method_errors():
    (response,) = rpc({"jsonrpc": "2.0", "id": 5, "method": "bogus/method"})
    assert response["error"]["code"] == -32601


def test_bad_tool_arguments_report_is_error():
    (response,) = rpc(
        {
            "jsonrpc": "2.0",
            "id": 7,
            "method": "tools/call",
            "params": {"name": "vet_dependency", "arguments": {"ecosystem": "npm"}},
        }
    )
    assert response["result"]["isError"] is True
