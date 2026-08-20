from __future__ import annotations

import json
import selectors
import subprocess
import sys
import time


APP_URI = "ui://cad/app.html"


def send(process: subprocess.Popen[bytes], message: dict[str, object]) -> None:
    assert process.stdin is not None
    process.stdin.write((json.dumps(message) + "\n").encode())
    process.stdin.flush()


def receive(process: subprocess.Popen[bytes], selector: selectors.BaseSelector, request_id: int) -> dict[str, object]:
    assert process.stdout is not None
    deadline = time.monotonic() + 30
    while selector.select(timeout=max(0, deadline - time.monotonic())):
        response = json.loads(process.stdout.readline())
        if response.get("id") == request_id:
            return response
    raise RuntimeError(f"no response for MCP request {request_id}")


def main() -> None:
    process = subprocess.Popen(
        sys.argv[1:],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=False,
    )
    assert process.stdout is not None
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)
    error: Exception | None = None
    try:
        send(process, {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-03-26",
                "capabilities": {
                    "extensions": {
                        "io.modelcontextprotocol/ui": {
                            "mimeTypes": ["text/html;profile=mcp-app"],
                        }
                    }
                },
                "clientInfo": {"name": "brigade-cad-smoke", "version": "1"},
            },
        })
        if "result" not in receive(process, selector, 1):
            raise RuntimeError("initialize returned no result")
        send(process, {"jsonrpc": "2.0", "method": "notifications/initialized"})
        send(process, {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
        listed = receive(process, selector, 2).get("result", {})
        tools = listed.get("tools", []) if isinstance(listed, dict) else []
        entry = next((tool for tool in tools if tool.get("name") == "cad.open"), None)
        if entry is None or entry.get("_meta", {}).get("ui", {}).get("resourceUri") != APP_URI:
            raise RuntimeError("cad.open with ui:// resource is absent from tools/list")
        send(process, {
            "jsonrpc": "2.0",
            "id": 3,
            "method": "resources/read",
            "params": {"uri": APP_URI},
        })
        resource = receive(process, selector, 3).get("result", {})
        if not isinstance(resource, dict) or not resource.get("contents"):
            raise RuntimeError(f"MCP App resource {APP_URI} is unavailable")
    except Exception as reason:
        error = reason
    finally:
        if process.poll() is None:
            process.terminate()
        _, stderr = process.communicate(timeout=5)
    if error is not None:
        raise RuntimeError(
            f"CAD MCP smoke failed ({process.returncode}): {error}: {stderr.decode().strip()}"
        ) from error


if __name__ == "__main__":
    main()
