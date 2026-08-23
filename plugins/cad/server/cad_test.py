from __future__ import annotations

import importlib
import os
import sys
import tempfile
import unittest
from pathlib import Path


class CADServerTest(unittest.TestCase):
    def test_pipeline_parameters_validation_and_revisions(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            os.environ["BRIGADE_WORKSPACE"] = directory
            os.environ["BRIGADE_SESSION_ID"] = "test-session"
            sys.path.insert(0, str(Path(__file__).parent))
            cad = importlib.import_module("cad")

            source = 'width = params.get("width", 10)\nresult = Box(width, 20, 30)'
            parameters = [{"id": "width", "label": "Width", "value": 10, "min": 5, "max": 40, "unit": "mm"}]
            state = cad.build_cad(source, "box", parameters)

            self.assertEqual(state["status"], "ready")
            self.assertEqual(state["revision"], 1)
            self.assertEqual(state["validation"]["status"], "pass")
            self.assertEqual(state["validation"]["bounds"]["x"], 10)
            self.assertTrue((Path(directory) / "box.py").is_file())
            self.assertTrue((Path(directory) / "box.step").is_file())
            self.assertTrue((Path(directory) / "box.glb").is_file())

            updated = cad.update_parameters({"width": 15})
            self.assertEqual(updated["revision"], 2)
            self.assertEqual(updated["parameters"][0]["value"], 15)
            self.assertEqual(updated["validation"]["bounds"]["x"], 15)
            self.assertEqual(len(updated["revisions"]), 2)

            restored = cad.restore_revision(1)
            self.assertEqual(restored["revision"], 3)
            self.assertEqual(restored["parameters"][0]["value"], 10)

            with self.assertRaises(ValueError):
                cad.build_cad("result = None", "box")
            self.assertEqual(cad.current_model()["status"], "error")
            self.assertEqual((Path(directory) / "box.py").read_text(), source)
            self.assertTrue(cad.read_preview()["data"])


if __name__ == "__main__":
    unittest.main()
