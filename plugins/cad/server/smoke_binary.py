from __future__ import annotations

import json
import selectors
import subprocess
import sys


def main() -> None:
    request = json.dumps(
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-03-26",
                "capabilities": {},
                "clientInfo": {"name": "brigade-cad-smoke", "version": "1"},
            },
        }
    )
    process = subprocess.Popen(
        [sys.argv[1]],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=False,
    )
    assert process.stdin is not None
    assert process.stdout is not None
    process.stdin.write((request + "\n").encode())
    process.stdin.flush()

    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)
    ready = selector.select(timeout=30)
    line = process.stdout.readline() if ready else b""
    response = json.loads(line) if line else None
    if process.poll() is None:
        process.terminate()
    _, stderr = process.communicate(timeout=5)
    if not isinstance(response, dict) or response.get("id") != 1 or "result" not in response:
        raise RuntimeError(
            f"CAD MCP initialize failed ({process.returncode}): {stderr.decode().strip()}"
        )


if __name__ == "__main__":
    main()
