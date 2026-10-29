import asyncio
import base64
from contextlib import chdir, redirect_stdout
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import sys
import subprocess
import tempfile
import threading
import tomllib
import unittest
import urllib.error
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

HERE = Path(__file__).resolve().parents[1]
REPO = HERE.parent.parent
sys.path[:0] = [str(HERE), str(HERE.parent / "harbor")]

import dataset
import gaia_runner
import vista_runner
from benchmark_profile import digest
from gaia_agent import GAIAVistaAgent
from summarize import summarize_run
from vista_agent import VistaAgent

spec = importlib.util.spec_from_file_location("gaia_run", HERE / "run.py")
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)

PNG = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=")
TASK = "00000000-0000-4000-8000-000000000001"


class GAIATest(unittest.TestCase):
    def setUp(self):
        state = REPO / ".vista/test-state/gaia"
        state.mkdir(parents=True, exist_ok=True)
        self.temporary = tempfile.TemporaryDirectory(dir=state)
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        previous_umask = os.umask(0o077)
        self.addCleanup(os.umask, previous_umask)

    def write(self, path, value):
        path = self.root / path
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value))
        return path

    def prepared(self):
        attachment = self.root / "fixture.png"
        attachment.write_bytes(PNG)
        row = {"task_id": TASK, "Level": 2, "Question": "Synthetic image question", "Final answer": "fixture-reference-answer", "file_name": attachment.name}
        task = dataset.write_task(self.root / "dataset" / TASK, row, attachment,
                                  "def question_scorer(answer, expected): return answer == expected\n")
        files = {str(path.relative_to(self.root / "dataset")): digest(path)
                 for path in (self.root / "dataset").rglob("*") if path.is_file()}
        self.write("dataset/manifest.json", {"sources": dataset.SOURCES, "tasks": [task], "files": files})
        return self.root / "dataset"

    def configuration(self):
        connection = "00000000-0000-4000-8000-000000000002"
        self.write("profile/models.json", {"default": "fixture", "models": [
            {"key": "fixture", "id": "fixture-model", "providerId": "openai-codex", "connectionId": connection},
            {"key": "unrelated", "id": "other", "apiKey": "unrelated-fixture-key"},
        ]})
        self.write("profile/codex-auth/" + connection + ".json", {
            "accountId": "fixture-account", "accessToken": "fixture-access", "refreshToken": "fixture-refresh", "expiresAt": "2030-01-01T00:00:00Z",
        })
        self.write("profile/search.json", {"fixture": "network-settings"})
        self.write("profile/memory.json", {"private": "must-not-transfer"})
        (self.root / "binary").write_bytes(b"\x7fELFfixture")
        return connection

    def args(self):
        return ["--dataset", str(self.root / "dataset"), "--binary", "candidate=" + str(self.root / "binary"),
                "--profile", str(self.root / "profile"), "--model", "fixture-model", "--output", str(self.root / "run")]

    def test_task_exposes_only_question_and_original_attachment_before_verification(self):
        directory = self.prepared()
        task = directory / TASK
        from harbor.models.task.config import TaskConfig
        config = TaskConfig.model_validate(tomllib.loads((task / "task.toml").read_text()))
        self.assertEqual(config.agent.timeout_sec, 600)
        self.assertEqual(config.verifier.timeout_sec, 300)
        self.assertEqual((task / "environment/workspace/fixture.png").read_bytes(), PNG)
        self.assertNotIn("fixture-reference-answer", (task / "instruction.md").read_text())
        self.assertEqual((task / "tests/expected_answer.txt").read_text(), "fixture-reference-answer")
        self.assertNotIn("tests", (task / "environment/Dockerfile").read_text())
        self.assertIn("/app/answer.txt", (task / "instruction.md").read_text())
        dataset.check_dataset(directory)
        (task / "tests/expected_answer.txt").write_text("tampered")
        with self.assertRaisesRegex(ValueError, "changed"):
            dataset.check_dataset(directory)

    def test_instruction_matches_the_original_full_evaluation_template(self):
        self.prepared()
        instruction = self.root / "dataset" / TASK / "instruction.md"
        self.assertEqual(digest(instruction), "256bf6346325bf7d6256a74d353e1cbb952cfae9e309f704b87553503e4aa774")
        row = {"task_id": TASK, "Level": 1, "Question": "Synthetic question", "Final answer": "fixture-answer"}
        dataset.write_task(self.root / "without-attachment", row, None, "")
        self.assertEqual(digest(self.root / "without-attachment/instruction.md"),
                         "b50d423af07c204d23256e8e6c2a871e10b25d942f6fb23f719da03f7c3bf640")

    def test_changed_instruction_template_is_rejected_before_running(self):
        directory = self.prepared()
        (self.root / "instruction.txt").write_text("A simplified task template")
        with patch.object(dataset, "HERE", self.root):
            with self.assertRaisesRegex(ValueError, "instruction template hash"):
                dataset.check_dataset(directory)

    def test_image_transport_preserves_originals_for_both_binary_interfaces(self):
        path = self.root / "original.png"
        path.write_bytes(PNG)
        image = {"path": str(path), "mediaType": "image/png", "name": path.name}
        ref = {"hash": hashlib.sha256(PNG).hexdigest(), "bytes": len(PNG), "mediaType": "image/png", "name": path.name}
        def upload(request, timeout):
            self.assertEqual(request.data.read(), PNG)
            self.assertEqual(timeout, 60)
            return io.BytesIO(json.dumps(ref).encode())
        with patch.object(vista_runner.urllib.request, "urlopen", side_effect=upload):
            self.assertEqual(vista_runner.upload_image("http://127.0.0.1:1", TASK, image), ref)
        missing = urllib.error.HTTPError("fixture", 404, "Not Found", {}, None)
        self.addCleanup(missing.close)
        with patch.object(vista_runner.urllib.request, "urlopen", side_effect=missing):
            inline = vista_runner.upload_image("http://127.0.0.1:1", TASK, image)
        self.assertEqual(base64.b64decode(inline["data"]), PNG)
        self.assertEqual(inline["name"], path.name)
        self.assertEqual(inline["mediaType"], "image/png")

    def test_image_transport_does_not_bypass_current_binary_rejections(self):
        path = self.root / "original.png"
        path.write_bytes(PNG)
        image = {"path": str(path), "mediaType": "image/png", "name": path.name}
        for status in (400, 403, 413, 503):
            with self.subTest(status=status):
                error = urllib.error.HTTPError("fixture", status, "Rejected", {}, None)
                self.addCleanup(error.close)
                with patch.object(vista_runner.urllib.request, "urlopen", side_effect=error) as upload:
                    with self.assertRaises(urllib.error.HTTPError):
                        vista_runner.upload_image("http://127.0.0.1:1", TASK, image)
                upload.assert_called_once()

    def test_original_images_are_unchanged_and_oversized_images_are_never_resized(self):
        (self.root / "original.png").write_bytes(PNG)
        (self.root / "notes.txt").write_text("not an image")
        images = gaia_runner.image_inputs(self.root)
        self.assertEqual(len(images), 1)
        self.assertEqual(Path(images[0]["path"]).read_bytes(), PNG)
        self.assertEqual(images[0]["mediaType"], "image/png")
        (self.root / "large.jpg").write_bytes(b"x" * (dataset.MAX_IMAGE_BYTES + 1))
        with self.assertRaisesRegex(ValueError, "50 MiB"):
            gaia_runner.image_inputs(self.root)

    def test_full_validation_counts_and_attachment_names_are_checked(self):
        rows = [{"task_id": f"00000000-0000-4000-8000-{index:012d}", "Question": "Synthetic question", "Final answer": "Synthetic answer",
                 "Level": 1 if index < 53 else 2 if index < 139 else 3,
                 "file_name": "fixture.txt" if index < 38 else ""} for index in range(165)]
        dataset.validate_records(rows)
        with self.assertRaises(ValueError):
            dataset.validate_records(rows[:-1])
        rows[0]["file_name"] = "../outside.txt"
        with self.assertRaisesRegex(ValueError, "plain filename"):
            dataset.validate_records(rows)

    def test_preparation_uses_pinned_downloads_and_keeps_data_out_of_the_build_context(self):
        import pyarrow as arrow
        import pyarrow.parquet as parquet
        row = {"task_id": TASK, "Level": 1, "Question": "Synthetic question", "Final answer": "fixture-answer", "file_name": "original.png"}
        metadata = self.root / "metadata.parquet"
        parquet.write_table(arrow.Table.from_pylist([row]), metadata)
        scorer = self.root / "scorer.py"
        scorer.write_text("import numpy as np\ndef question_scorer(answer, expected): return answer == expected\n")
        attachment = self.root / "original.png"
        attachment.write_bytes(PNG)
        sources = {"instruction": dataset.SOURCES["instruction"],
                   "dataset": {**dataset.SOURCES["dataset"], "sha256": digest(metadata), "levels": {"1": 1}, "attachments": 1},
                   "scorer": {**dataset.SOURCES["scorer"], "sha256": digest(scorer)}}
        downloads = {"2023/validation/metadata.parquet": metadata, "scorer.py": scorer, "2023/validation/original.png": attachment}
        with patch.object(dataset, "SOURCES", sources), \
             patch("huggingface_hub.hf_hub_download", side_effect=lambda **args: downloads[args["filename"]]) as download:
            directory = self.root / "prepared"
            dataset.prepare(directory, self.root / "cache", offline=True)
            manifest = dataset.check_dataset(directory)
            self.assertEqual(len(manifest["tasks"]), 1)
            for call in download.call_args_list:
                self.assertTrue(call.kwargs["local_files_only"])
                expected = sources["scorer" if call.kwargs["repo_type"] == "space" else "dataset"]
                self.assertEqual(call.kwargs["revision"], expected["revision"])
            self.assertNotIn("import numpy", (directory / TASK / "tests/scorer.py").read_text())
            self.assertEqual((directory / TASK / "environment/workspace/original.png").read_bytes(), PNG)
            self.assertFalse(list((directory / TASK / "environment").rglob("*answer*")))
            with self.assertRaisesRegex(ValueError, "already exists"):
                dataset.prepare(directory, self.root / "cache", offline=True)
            sources["scorer"]["sha256"] = "incorrect-hash"
            with self.assertRaisesRegex(ValueError, "hash differs"):
                dataset.prepare(self.root / "bad", self.root / "cache", offline=True)
            self.assertFalse((self.root / "bad").exists())

    def test_dry_run_does_not_write_output_or_start_docker_or_a_model(self):
        self.prepared()
        self.configuration()
        with patch.object(runner.subprocess, "run") as process, patch.object(runner.subprocess, "check_output") as probe, \
             redirect_stdout(io.StringIO()) as output:
            runner.main([*self.args(), "--dry-run", "--reasoning-effort", "none"])
        plan = json.loads(output.getvalue())
        self.assertFalse((self.root / "run").exists())
        process.assert_not_called()
        probe.assert_not_called()
        self.assertEqual(plan["suite"], "gaia")
        self.assertIn("subset", plan["scope"])
        self.assertIn("reasoning_effort=none", plan["commands"][0])
        self.assertEqual(plan["commands"][0][plan["commands"][0].index("-r") + 1], "0")
        with self.assertRaises(ValueError):
            runner.select_tasks({"tasks": [{"task": TASK, "level": 2}]}, ["missing"])

    def test_execution_snapshots_only_selected_configuration_and_preserves_source_credentials(self):
        self.prepared()
        connection = self.configuration()
        source_auth = self.root / "profile/codex-auth" / (connection + ".json")
        original = source_auth.read_bytes()
        with patch.object(runner.importlib.metadata, "version", return_value=runner.HARBOR_VERSION), \
             patch.object(runner.subprocess, "check_output", return_value="fixture-version\n"), \
             patch.object(runner, "execute") as execute:
            runner.main(self.args())
        output, plan = execute.call_args.args
        selected = json.loads((output / "profile/models.json").read_text())
        self.assertEqual(len(selected["models"]), 1)
        self.assertFalse((output / "profile/memory.json").exists())
        self.assertTrue((output / "profile/search.json").is_file())
        self.assertEqual((output / "binary/candidate").read_bytes(), (self.root / "binary").read_bytes())
        self.assertFalse(any("codex-auth" in path for path in plan["frozen_sha256"]))
        (output / "profile/codex-auth" / source_auth.name).write_text("updated isolated state")
        self.assertEqual(source_auth.read_bytes(), original)
        (self.root / "dataset" / TASK / "instruction.md").write_text("changed task")
        with patch.object(runner.subprocess, "run") as process, redirect_stdout(io.StringIO()):
            with self.assertRaisesRegex(RuntimeError, "Frozen"):
                runner.execute(output, plan)
        process.assert_not_called()
        summary = summarize_run(output)
        self.assertEqual(summary["suite"], "gaia")
        self.assertEqual(summary["levels"]["2"]["planned"], 1)
        self.assertEqual(summary["totals"]["candidate"]["levels"]["2"]["planned"], 1)
        self.assertEqual(summary["totals"]["candidate"]["states"], {"missing_result": 1})
        self.assertIn("not contamination-controlled", summary["scope"])

    def test_oauth_refresh_updates_only_run_local_state_and_rejects_another_account(self):
        connection = self.configuration()
        profile = self.root / "profile"
        adapter = GAIAVistaAgent(logs_dir=self.root / "logs", model_name="fixture-model",
                                 binary_path=str(self.root / "binary"), profile_path=str(profile))
        auth = profile / "codex-auth" / (connection + ".json")
        updated = {**json.loads(auth.read_text()), "accessToken": "refreshed-fixture"}
        async def download(source, target):
            target.write_text(json.dumps(updated))
        environment = AsyncMock()
        environment.download_file.side_effect = download
        with patch.object(VistaAgent, "run", new_callable=AsyncMock), patch.object(adapter.logger, "warning") as warning:
            asyncio.run(adapter.run("fixture task", environment, None))
            self.assertEqual(json.loads(auth.read_text()), updated)
            updated = {**updated, "accountId": "other-account", "accessToken": "must-reject"}
            asyncio.run(adapter.run("fixture task", environment, None))
            warning.assert_called_once()
        self.assertEqual(json.loads(auth.read_text())["accessToken"], "refreshed-fixture")

    def test_installed_runner_is_self_contained(self):
        self.configuration()
        adapter = GAIAVistaAgent(logs_dir=self.root / "logs", model_name="fixture-model",
                                 binary_path=str(self.root / "binary"), profile_path=str(self.root / "profile"))
        installed = self.root / "installed"
        async def upload(source, target):
            path = installed / target.removeprefix("/installed-agent/vista-eval/")
            path.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, path)
        environment = AsyncMock()
        environment.default_user = None
        environment.upload_file.side_effect = upload
        environment.exec.return_value = SimpleNamespace(return_code=0, stdout="apt-get", stderr="")
        asyncio.run(adapter.install(environment))
        completed = subprocess.run([sys.executable, str(installed / "runner.py"), "--help"],
                                   capture_output=True, text=True, cwd=installed, env={**os.environ, "PYTHONPATH": ""})
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("--logs-dir", completed.stdout)
        self.assertTrue((installed / "xdg/vista/search.json").is_file())

    @unittest.skipUnless(os.environ.get("VISTA_BENCHMARK_BIN"), "Set VISTA_BENCHMARK_BIN for an isolated application check")
    def test_real_vista_receives_original_image_and_stops_without_a_live_provider(self):
        requests = []
        image_hash = hashlib.sha256(PNG).hexdigest()
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                requests.append(body)
                images = [part for message in body.get("messages", []) if isinstance(message.get("content"), list)
                          for part in message["content"] if part.get("type") == "image_url"]
                if body.get("tools") and not images and not any(message.get("role") == "tool" for message in body["messages"]):
                    delta = {"tool_calls": [{"index": 0, "id": "fixture-read", "type": "function", "function": {
                        "name": "read_resource", "arguments": json.dumps({"hash": image_hash}),
                    }}]}
                    reason = "tool_calls"
                else:
                    delta, reason = {"content": "Fixture completed"}, "stop"
                event = {"choices": [{"delta": delta, "finish_reason": reason}], "usage": {"prompt_tokens": 10, "completion_tokens": 4}}
                payload = ("data: " + json.dumps(event) + "\n\ndata: [DONE]\n\n").encode()
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(thread.join, 5)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        shutil.copyfile(os.environ["VISTA_BENCHMARK_BIN"], self.root / "vista")
        (self.root / "vista").chmod(0o700)
        self.write("models.json", {"default": "fixture", "models": [{"key": "fixture", "provider": "openai-chat", "providerId": "custom",
                   "authentication": "bearer", "id": "fixture-model", "baseUrl": f"http://127.0.0.1:{server.server_port}/v1",
                   "apiKey": "fixture-key", "inputModalities": ["text", "image"]}]})
        (self.root / "instruction.md").write_text("Inspect the original image and finish.")
        (self.root / "files").mkdir()
        (self.root / "files/original.png").write_bytes(PNG)
        with chdir(self.root):
            gaia_runner.execute(self.root, self.root / "logs", files=self.root / "files")
        urls = [part["image_url"]["url"] for body in requests for message in body.get("messages", [])
                if isinstance(message.get("content"), list) for part in message["content"] if part.get("type") == "image_url"]
        self.assertTrue(urls)
        self.assertTrue(all(base64.b64decode(url.split(",", 1)[1]) == PNG for url in urls))
        self.assertFalse((self.root / "xdg/vista/state/instance.json").exists())
        self.assertEqual(json.loads((self.root / "logs/gaia-image-input.json").read_text())["images"][0]["bytes"], len(PNG))


if __name__ == "__main__":
    unittest.main()
