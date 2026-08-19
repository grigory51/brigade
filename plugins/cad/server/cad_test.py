from __future__ import annotations

import importlib
import os
import sys
import tempfile
import unittest
from pathlib import Path


class CADServerTest(unittest.TestCase):
    def test_build_writes_source_step_and_preview(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            os.environ["BRIGADE_WORKSPACE"] = directory
            os.environ["BRIGADE_SESSION_ID"] = "test-session"
            sys.path.insert(0, str(Path(__file__).parent))
            cad = importlib.import_module("cad")

            state = cad.build_cad("result = Box(10, 20, 30)", "box")

            self.assertEqual(state["status"], "ready")
            self.assertTrue((Path(directory) / "box.py").is_file())
            self.assertTrue((Path(directory) / "box.step").is_file())
            self.assertTrue((Path(directory) / "box.glb").is_file())

            with self.assertRaises(ValueError):
                cad.build_cad("result = None", "box")
            self.assertEqual((Path(directory) / "box.py").read_text(), "result = Box(10, 20, 30)")


if __name__ == "__main__":
    unittest.main()
