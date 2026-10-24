"""Container-side stdlib client. Vista, not this adapter, performs the task."""

import argparse
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import time
import urllib.error
import urllib.request


ROOT = Path(__file__).resolve().parent


def environment(root):
    # Import network settings, but no host model keys, config paths or prompts.
    allowed = {"PATH", "LANG", "LC_ALL", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
               "http_proxy", "https_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"}
    env = {key: value for key, value in os.environ.items() if key in allowed}
    env.update(HOME=str(root / "home"), XDG_CONFIG_HOME=str(root / "xdg"),
               XDG_DATA_HOME=str(root / "data"), XDG_STATE_HOME=str(root / "state"),
               XDG_CACHE_HOME=str(root / "cache"), TMPDIR=str(root / "tmp"), VISTA_NO_BROWSER="1")
    return env


def stop(root):
    if (root / "xdg/vista/state/instance.json").is_file():
        return subprocess.run([str(root / "vista"), "stop"], env=environment(root),
                              capture_output=True, text=True, timeout=18)


def prompt_for(instruction, directory, instruction_path):
    prefix = f"Environment working directory: {directory}\n\n"
    prompt = prefix + instruction
    if len(prompt.encode()) <= 8192:
        return prompt, "complete task text with working directory"
    return (prefix + f"Complete the task in {instruction_path}. Read the entire instruction file before acting.",
            "complete file reference (API 8192-byte limit)")


def request(address, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(address + "/api" + path, data=data,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=15) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        # Provider details remain in the isolated event log, not exception text.
        raise RuntimeError(f"Vista API {path}: HTTP {error.code}") from error


def execute(root, logs, effort=None, image_loader=None):
    config = root / "xdg/vista"
    logs.mkdir(parents=True, exist_ok=True)
    env = environment(root)
    for name in ("home", "xdg/vista", "data", "state", "cache", "tmp"):
        (root / name).mkdir(parents=True, exist_ok=True, mode=0o700)
    shutil.copyfile(root / "models.json", config / "models.json")
    (config / "models.json").chmod(0o600)
    if (root / "omp-models.json").is_file():
        shutil.copyfile(root / "omp-models.json", config / "omp-models.json")
    (config / "model-catalog-status.json").write_text(json.dumps({
        "nextCheckAt": (datetime.now(timezone.utc) + timedelta(days=7)).isoformat(),
    }))
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        env["VISTA_PORT"] = str(sock.getsockname()[1])
    started = time.monotonic()
    summary = {"working_directory": os.getcwd(), "reasoning_effort": effort}
    try:
        result = subprocess.run([str(root / "vista"), "start"], env=env,
                                capture_output=True, text=True, timeout=25)
        (logs / "vista-start.txt").write_text(result.stdout + result.stderr)
        if result.returncode:
            raise RuntimeError("Could not start isolated Vista; see vista-start.txt")
        address = json.loads((config / "state/instance.json").read_text())["address"]
        status = request(address, "/status")
        summary["build"] = {key: status.get(key) for key in ("version", "sourceCommit", "sourceModified", "binaryHash", "hostOS")}
        session = request(address, "/sessions", {"title": "Harbor evaluation"})
        prompt, summary["instruction_delivery"] = prompt_for(
            (root / "instruction.md").read_text(), Path.cwd(), root / "instruction.md")
        (logs / "submitted-prompt.txt").write_text(prompt)
        selected = json.loads((config / "models.json").read_text())
        body = {"content": prompt, "selectionMode": "manual", "modelKey": selected["default"]}
        if effort is not None:
            body["reasoningEffort"] = effort
        if image_loader is not None:
            images = image_loader()
            if images:
                body["images"] = images
        sent = request(address, f"/sessions/{session['id']}/messages", body)
        summary.update(session_id=session["id"], run_id=sent["runId"])
        cursor, finished = 0, None
        with (logs / "vista-events.jsonl").open("a", buffering=1) as trace:
            while finished is None:
                page = request(address, f"/sessions/{session['id']}/event-page?after={cursor}")
                for event in page:
                    cursor = event["sequence"]
                    trace.write(json.dumps(event, ensure_ascii=False) + "\n")
                    data = event.get("data", {})
                    if event["kind"] == "run_finished" and data.get("runId") == sent["runId"]:
                        finished = data
                if finished is None:
                    time.sleep(0.5)
        summary["run_result"] = finished
        (logs / "vista-messages.json").write_text(json.dumps(
            request(address, f"/sessions/{session['id']}/messages"), ensure_ascii=False, indent=2))
        if finished.get("status") != "succeeded" or finished.get("pendingReply"):
            raise RuntimeError("Vista did not finish autonomously; see vista-events.jsonl")
    except Exception as error:
        summary["runner_error"] = type(error).__name__
        raise
    finally:
        summary["elapsed_s"] = round(time.monotonic() - started, 3)
        (logs / "vista-summary.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2))
        try:
            stopped = stop(root)
            if stopped is not None:
                (logs / "vista-stop.txt").write_text(stopped.stdout + stopped.stderr)
        finally:
            if (config / "state/server.log").is_file():
                shutil.copyfile(config / "state/server.log", logs / "vista-server.log")
            if (config / "state/outputs").is_dir():
                shutil.copytree(config / "state/outputs", logs / "vista-artifacts", dirs_exist_ok=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--logs-dir", type=Path, default=Path("/logs/agent"))
    parser.add_argument("--effort")
    parser.add_argument("--stop", action="store_true")
    args = parser.parse_args()
    if args.stop:
        stop(ROOT)
    else:
        execute(ROOT, args.logs_dir, args.effort)
