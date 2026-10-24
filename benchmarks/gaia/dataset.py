"""Prepare pinned, gated GAIA validation data locally in Harbor task format."""

from collections import Counter
import json
from pathlib import Path
import shutil
import tempfile
from uuid import UUID

from benchmark_profile import digest


HERE = Path(__file__).resolve().parent
SOURCES = json.loads((HERE / "sources.json").read_text())
IMAGE_TYPES = {".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
               ".webp": "image/webp", ".gif": "image/gif"}
MAX_IMAGE_BYTES = 1 << 20  # internal/store/images.go; preserve originals, never resize.

VERIFIER = """#!/bin/bash
set -euo pipefail
mkdir -p /logs/verifier
python3 - <<'PY'
from pathlib import Path
import sys
sys.path.insert(0, '/tests')
from scorer import question_scorer
answer = Path('/app/answer.txt')
passed = answer.is_file() and question_scorer(answer.read_text().strip(), Path('/tests/expected_answer.txt').read_text().strip())
Path('/logs/verifier/reward.txt').write_text('1' if passed else '0')
PY
"""


def validate_records(rows):
    ids = []
    for row in rows:
        ids.append(str(UUID(row["task_id"])))
        if ids[-1] != row["task_id"] or not row["Question"].strip() or not row["Final answer"].strip():
            raise ValueError("Invalid GAIA task record")
        name = row.get("file_name") or ""
        if name and (Path(name).name != name or name in (".", "..") or "\\" in name):
            raise ValueError("GAIA attachment must have a plain filename")
    counts = Counter(str(int(row["Level"])) for row in rows)
    if len(set(ids)) != len(ids) or counts != SOURCES["dataset"]["levels"]:
        raise ValueError("GAIA validation task IDs or level counts differ from sources.json")
    if sum(bool(row.get("file_name")) for row in rows) != SOURCES["dataset"]["attachments"]:
        raise ValueError("GAIA validation attachment count differs from sources.json")


def write_task(directory, row, attachment, scorer):
    """Only the question and original attachment enter the agent environment."""
    environment, tests = directory / "environment", directory / "tests"
    environment.mkdir(parents=True)
    tests.mkdir()
    dockerfile = "FROM python:3.11-slim-bookworm\nWORKDIR /app\nRUN apt-get update && apt-get install -y --no-install-recommends curl && rm -rf /var/lib/apt/lists/*\n"
    instruction = "# GAIA task\n\n" + row["Question"]
    name = row.get("file_name") or ""
    if name:
        (environment / "workspace").mkdir()
        shutil.copyfile(attachment, environment / "workspace" / name)
        dockerfile += "COPY workspace/ /app/files/\n"
        instruction += f"\n\nOriginal attachment: `/app/files/{name}`."
    instruction += "\n\nWrite ONLY the final answer to `/app/answer.txt`, using the format requested by the question. Do not include an explanation in that file.\n"
    (directory / "instruction.md").write_text(instruction)
    (environment / "Dockerfile").write_text(dockerfile)
    level = int(row["Level"])
    (directory / "task.toml").write_text(f'''version = "1.0"
[metadata]
source = "gaia-benchmark/GAIA"
difficulty = "{ {1: 'easy', 2: 'medium', 3: 'hard'}[level]}"
category = "reasoning"
tags = ["gaia", "level{level}", "tool-use"]
[verifier]
timeout_sec = 300.0
[agent]
timeout_sec = 600.0
[environment]
build_timeout_sec = 300.0
''')
    (tests / "expected_answer.txt").write_text(row["Final answer"])
    (tests / "scorer.py").write_text(scorer)
    (tests / "test.sh").write_text(VERIFIER)
    (tests / "test.sh").chmod(0o700)
    return {"task": row["task_id"], "level": level, "attachment": name or None,
            "input_image_over_limit": bool(name and Path(name).suffix.lower() in IMAGE_TYPES
                                           and attachment.stat().st_size > MAX_IMAGE_BYTES)}


def prepare(directory, cache, offline=False):
    from huggingface_hub import hf_hub_download
    import pyarrow.parquet as parquet

    if directory.exists():
        raise ValueError("Dataset directory already exists; use it or choose a new --dataset")
    source = SOURCES["dataset"]
    def download(repo, filename, revision, repo_type):
        return Path(hf_hub_download(repo_id=repo, filename=filename, revision=revision, repo_type=repo_type,
                                   cache_dir=cache, local_files_only=offline))
    metadata = download(source["repo"], source["metadata"], source["revision"], "dataset")
    scorer_source = SOURCES["scorer"]
    scorer = download(scorer_source["repo"], scorer_source["filename"], scorer_source["revision"], "space")
    if digest(metadata) != source["sha256"] or digest(scorer) != scorer_source["sha256"]:
        raise ValueError("GAIA metadata or official scorer hash differs from sources.json")
    rows = sorted(parquet.read_table(metadata).to_pylist(), key=lambda row: row["task_id"])
    validate_records(rows)
    scoring_code = scorer.read_text().replace("import numpy as np\n", "")
    directory.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=directory.parent, prefix=".gaia-prepare-") as temporary:
        staging = Path(temporary) / "dataset"
        staging.mkdir(mode=0o700)
        tasks = []
        for row in rows:
            name = row.get("file_name")
            attachment = download(source["repo"], "2023/validation/" + name, source["revision"], "dataset") if name else None
            tasks.append(write_task(staging / row["task_id"], row, attachment, scoring_code))
        files = {str(path.relative_to(staging)): digest(path) for path in sorted(staging.rglob("*")) if path.is_file()}
        manifest = {"sources": SOURCES, "tasks": tasks, "files": files,
                    "scorer_adjustment": "Remove unused numpy import; scoring functions unchanged"}
        (staging / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        staging.rename(directory)
    return manifest


def check_dataset(directory):
    manifest = json.loads((directory / "manifest.json").read_text())
    if manifest["sources"] != SOURCES:
        raise ValueError("Prepared GAIA dataset differs from sources.json; prepare a new dataset directory")
    for name, expected in manifest["files"].items():
        path = (directory / name).resolve()
        if not path.is_relative_to(directory.resolve()) or digest(path) != expected:
            raise ValueError("Prepared GAIA task file changed: " + name)
    return manifest
