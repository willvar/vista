import argparse
from contextlib import chdir
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parents[1]
REPO = HERE.parent.parent
sys.path.insert(0, str(HERE))

from benchmark_profile import selected_profile
from run import command, schedule
from summarize import summarize_run, trial, vista_usage
import vista_runner


class BenchmarkTest(unittest.TestCase):
    def setUp(self):
        state = REPO / ".vista/test-state/harbor"
        state.mkdir(parents=True, exist_ok=True)
        self.temporary = tempfile.TemporaryDirectory(dir=state)
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def write(self, relative, value):
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value))
        return path

    def test_only_the_selected_model_and_credential_are_transferred(self):
        connection = "00000000-0000-4000-8000-000000000001"
        selected = {"key": "selected", "id": "fixture-model", "providerId": "openai-codex",
                    "connectionId": connection, "codexCredentialPath": "/private/host/path"}
        self.write("models.json", {"default": "selected", "models": [selected, {"key": "other", "id": "other", "apiKey": "unrelated-fixture-key"}]})
        credential = self.write("codex-auth/" + connection + ".json", {"accessToken": "fixture-token"})
        config, credentials = selected_profile(self.root, "fixture-model")
        self.assertEqual(config["default"], "selected")
        self.assertEqual(len(config["models"]), 1)
        self.assertNotIn("codexCredentialPath", config["models"][0])
        self.assertEqual(credentials, [(credential, "codex-auth/" + connection + ".json")])
        with self.assertRaises(ValueError):
            selected_profile(self.root, "wrong-model")
        credential.unlink()
        with self.assertRaises(ValueError):
            selected_profile(self.root, "fixture-model")

    def test_usage_deduplicates_events_and_keeps_missing_usage_unknown(self):
        path = self.root / "events.jsonl"
        events = [
            {"sequence": 1, "kind": "model_request"},
            {"sequence": 2, "kind": "model_result", "data": {"modelResult": {
                "provider": "fixture", "id": "model", "usage": {"inputTokens": 10, "outputTokens": 4, "cacheReadTokens": 3},
            }}},
        ]
        path.write_text("".join(json.dumps(event) + "\n" for event in events + events))
        usage = vista_usage(path)
        self.assertTrue(usage["complete"])
        self.assertEqual(usage["models"]["fixture/model"], {"n_input_tokens": 10, "n_output_tokens": 4, "n_cache_tokens": 3})
        events.append({"sequence": 3, "kind": "model_request"})
        path.write_text("".join(json.dumps(event) + "\n" for event in events))
        self.assertFalse(vista_usage(path)["complete"])
        events.append({"sequence": 4, "kind": "model_result", "data": {"modelResult": {"provider": "fixture", "id": "model"}}})
        path.write_text("".join(json.dumps(event) + "\n" for event in events))
        self.assertEqual(vista_usage(path)["models"], {})
        self.assertEqual(vista_usage(path)["missing_usage"], 1)
        path.write_text("".join(json.dumps(event) + "\n" for event in events[:2]) + '{"sequence":')
        self.assertFalse(vista_usage(path)["complete"])

    def test_summaries_retain_failures_errors_and_missing_trials(self):
        entries = [{"round": 1, "task": name, "label": "candidate", "job": name} for name in ("pass", "fail", "error", "missing")]
        self.write("plan.json", {"dataset": "fixture", "dataset_commit": "fixture-revision", "model": "model",
                                 "reasoning_effort": None, "binaries": {}, "sequence": entries})
        for entry, reward, exception in zip(entries, (1, 0, None), (None, None, {"exception_type": "EnvironmentStartTimeoutError"})):
            base = "jobs/" + entry["job"] + "/trial/"
            self.write(base + "result.json", {"task_name": entry["task"], "verifier_result": {"rewards": {"reward": reward}},
                       "exception_info": exception, "agent_execution": {"started_at": "2026-01-01T00:00:00Z", "finished_at": "2026-01-01T00:00:02Z"}})
            self.write(base + "agent/vista-summary.json", {"run_result": {"status": "succeeded"}})
        summary = summarize_run(self.root)
        self.assertEqual(summary["totals"]["candidate"]["states"], {"passed": 1, "failed": 1, "error": 1, "missing_result": 1})
        self.assertEqual(summary["totals"]["candidate"]["successful_agent_seconds"], {"n": 1, "median": 2.0, "min": 2.0, "max": 2.0})
        self.assertIsNone(summary["trials"][0]["tokens"])
        self.assertIsNone(summary["trials"][0]["cost_usd"])
        self.write("jobs/pass/trial/agent/vista-summary.json", {"run_result": {"status": "succeeded", "pendingReply": True}})
        self.assertFalse(trial(self.root / "jobs/pass", entries[0])["success"])
        self.write("jobs/pass/trial/agent/vista-summary.json", {"run_result": {"status": "succeeded"}, "runner_error": "RuntimeError"})
        record = trial(self.root / "jobs/pass", entries[0])
        self.assertEqual(record["state"], "error")
        self.assertFalse(record["success"])

    def test_interleaving_preserves_original_attempt_and_timeout_policy(self):
        labels, tasks = ["before", "after", "reference"], ["task-a", "task-b", "task-c"]
        entries = schedule(labels, tasks, 3)
        self.assertEqual(len({entry["job"] for entry in entries}), 27)
        for task in tasks:
            first = [entries[index]["label"] for index in range(0, len(entries), 3) if entries[index]["task"] == task]
            self.assertEqual(set(first), set(labels))
        args = argparse.Namespace(dataset=self.root, profile=self.root / "profile with spaces", model="fixture-model", model_key=None, reasoning_effort="none")
        argv = command(entries[0], args, {label: {"path": "/binary with spaces"} for label in labels}, self.root)
        self.assertEqual(argv[argv.index("-n") + 1], "1")
        self.assertEqual(argv[argv.index("-k") + 1], "1")
        self.assertEqual(argv[argv.index("-r") + 1], "0")
        self.assertFalse(any("timeout" in value for value in argv))
        self.assertIn("reasoning_effort=none", argv)
        self.assertIn("profile_path=" + str(args.profile), argv)

    def test_long_unicode_instructions_remain_complete_and_host_config_is_not_inherited(self):
        instruction = "文字" * 5000
        prompt, kind = vista_runner.prompt_for(instruction, "/app", "/fixture/instruction.md")
        self.assertIn("/fixture/instruction.md", prompt)
        self.assertIn("file reference", kind)
        self.assertLessEqual(len(prompt.encode()), 8192)
        with patch.dict(os.environ, {"OPENAI_API_KEY": "fixture-host-key", "VISTA_PORT": "5186", "XDG_CONFIG_HOME": "/host/config"}):
            env = vista_runner.environment(self.root)
        self.assertNotIn("OPENAI_API_KEY", env)
        self.assertNotIn("VISTA_PORT", env)
        self.assertEqual(env["XDG_CONFIG_HOME"], str(self.root / "xdg"))

    def test_pending_human_interaction_does_not_become_a_successful_completion(self):
        self.write("models.json", {"default": "fixture"})
        self.write("xdg/vista/state/instance.json", {"address": "http://127.0.0.1:1"})
        (self.root / "instruction.md").write_text("Fixture task")
        replies = [{}, {"id": "session"}, {"runId": "run"}, [
            {"sequence": 1, "kind": "run_finished", "data": {"runId": "run", "status": "succeeded", "pendingReply": True}},
        ], []]
        with patch.object(vista_runner.subprocess, "run", return_value=SimpleNamespace(returncode=0, stdout="", stderr="")), \
             patch.object(vista_runner, "request", side_effect=replies) as api, \
             patch.object(vista_runner, "stop", return_value=None) as stop:
            with self.assertRaisesRegex(RuntimeError, "did not finish autonomously"):
                vista_runner.execute(self.root, self.root / "logs")
        stop.assert_called_once_with(self.root)
        self.assertNotIn("reasoningEffort", api.call_args_list[2].args[2])
        self.assertEqual(json.loads((self.root / "logs/vista-summary.json").read_text())["runner_error"], "RuntimeError")

    def test_adapter_uses_the_pinned_harbor_interface(self):
        from vista_agent import VistaAgent
        self.write("models.json", {"default": "fixture", "models": [{"key": "fixture", "id": "fixture"}]})
        (self.root / "vista").write_bytes(b"fixture binary")
        adapter = VistaAgent(logs_dir=self.root, model_name="fixture", binary_path=str(self.root / "vista"), profile_path=str(self.root))
        self.assertEqual(adapter.name(), "vista")
        self.assertIsNone(adapter.reasoning_effort)

    @unittest.skipUnless(os.environ.get("VISTA_BENCHMARK_BIN"), "Set VISTA_BENCHMARK_BIN for an isolated application smoke check")
    def test_real_vista_runs_tools_through_the_adapter_without_a_live_provider(self):
        requests = []
        workspace = self.root / "workspace"
        workspace.mkdir()

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                title = str(body["messages"][0]["content"]).startswith("Write a short, specific title")
                if not title:
                    requests.append(body)
                rules, pages = {}, {}
                for message in body["messages"]:
                    if message["role"] != "tool":
                        continue
                    try:
                        result = json.loads(message["content"])
                    except (ValueError, TypeError):
                        continue
                    if result.get("status") == "instructions_required":
                        rules.update((rule["path"], rule) for rule in result["instructions"])
                    if "sha256" in result and "nextOffset" in result:
                        pages[result["path"]] = result
                pending = next((rule for path, rule in rules.items()
                                if pages.get(path, {}).get("sha256") != rule["sha256"]
                                or pages[path]["truncated"]), None)
                if not title and (pending or not (workspace / "marker.txt").exists()):
                    name = "read_project_instruction" if pending else "shell"
                    arguments = ({"path": pending["path"], "offset": pages.get(pending["path"], {}).get("nextOffset", 0)}
                                 if pending else {"directory": str(workspace), "command": "printf 'benchmark fixture' > marker.txt"})
                    delta = {"tool_calls": [{"index": 0, "id": f"fixture-{len(body['messages'])}", "type": "function", "function": {
                        "name": name, "arguments": json.dumps(arguments),
                    }}]}
                    reason = "tool_calls"
                else:
                    delta, reason = {"content": "Fixture completed"}, "stop"
                event = {"choices": [{"delta": delta, "finish_reason": reason}], "usage": {"prompt_tokens": 12, "completion_tokens": 3}}
                payload = ("data: " + json.dumps(event) + "\n\ndata: [DONE]\n\n").encode()
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        shutil.copyfile(os.environ["VISTA_BENCHMARK_BIN"], self.root / "vista")
        (self.root / "vista").chmod(0o700)
        self.write("models.json", {"default": "fixture", "models": [{"key": "fixture", "provider": "openai-chat", "providerId": "custom",
                   "authentication": "bearer", "id": "fixture-model", "baseUrl": f"http://127.0.0.1:{server.server_port}/v1", "apiKey": "fixture-key"}]})
        (self.root / "instruction.md").write_text("Write marker.txt using the shell, then finish.")
        try:
            with chdir(workspace):
                vista_runner.execute(self.root, self.root / "logs")
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)
        self.assertEqual((workspace / "marker.txt").read_text(), "benchmark fixture")
        # Each tool turn must be followed by another model request, including
        # directory-rule reads and the shell retry after acknowledgment.
        self.assertEqual(len(requests), 1 + sum(message["role"] == "tool" for message in requests[-1]["messages"]))
        self.assertTrue(any(message["role"] == "tool" for message in requests[-1]["messages"]))
        usage = vista_usage(self.root / "logs/vista-events.jsonl")
        self.assertTrue(usage["complete"])
        summary = json.loads((self.root / "logs/vista-summary.json").read_text())
        self.assertEqual(summary["run_result"]["status"], "succeeded")
        self.assertTrue(summary["build"]["binaryHash"])
        self.assertFalse((self.root / "xdg/vista/state/instance.json").exists())


if __name__ == "__main__":
    unittest.main()
