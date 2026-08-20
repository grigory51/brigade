from __future__ import annotations

import json
import sys
import zipfile


with zipfile.ZipFile(sys.argv[1]) as package:
    manifest = json.loads(package.read("manifest.json"))
    assert manifest["version"] == sys.argv[2], manifest["version"]
    assert manifest["_meta"]["brigade"]["experience"]["entry_tool"] == "cad.open"
    assert manifest["server"]["entry_point"] in package.namelist()
    assert "ui/mcp-app.html" in package.namelist()
