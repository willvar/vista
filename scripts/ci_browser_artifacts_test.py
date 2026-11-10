from pathlib import Path
import tempfile
import unittest

from ci_browser_artifacts import collect

ROOT = Path(__file__).resolve().parent.parent


class BrowserArtifactsTest(unittest.TestCase):
    def setUp(self):
        (ROOT / ".vista").mkdir(exist_ok=True)

    def test_collects_first_failure_and_passing_retry_diagnostics(self):
        with tempfile.TemporaryDirectory(dir=ROOT / ".vista") as directory:
            source = Path(directory) / "source"
            output = Path(directory) / "collected"
            source.mkdir()
            for name in ("first.png", "error.md", "retry.zip", "unrelated.zip", "model.json"):
                (source / name).write_text(name)

            def attachment(name, filename):
                return {"name": name, "path": str(source / filename)}

            suite = {"suites": [{"specs": [{"tests": [
                {"results": [
                    {"status": "failed", "attachments": [
                        attachment("screenshot", "first.png"),
                        attachment("error-context", "error.md"),
                        attachment("request-body", "model.json"),
                    ]},
                    {"status": "passed", "attachments": [attachment("trace", "retry.zip")]},
                ]},
                {"results": [{"status": "passed", "attachments": [attachment("trace", "unrelated.zip")]}]},
            ]}]}]}
            self.assertEqual(collect(suite, source.resolve(), output), 3)
            self.assertEqual(sorted(path.name for path in output.iterdir()), ["error.md", "first.png", "retry.zip"])
            self.assertEqual((output / "retry.zip").read_text(), "retry.zip")

    def test_rejects_attachments_outside_the_browser_output(self):
        with tempfile.TemporaryDirectory(dir=ROOT / ".vista") as directory:
            source = Path(directory) / "source"
            source.mkdir()
            outside = Path(directory) / "private.txt"
            outside.write_text("must stay outside the upload")
            suite = {"specs": [{"tests": [{"results": [{"status": "failed", "attachments": [
                {"name": "trace", "path": str(outside)},
            ]}]}]}]}
            with self.assertRaises(ValueError):
                collect(suite, source.resolve(), Path(directory) / "collected")


if __name__ == "__main__":
    unittest.main()
