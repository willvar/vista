"""Select one model and its credential files without copying a user's profile."""

import hashlib
import json
from pathlib import Path
import re
from uuid import UUID


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def binaries(specs):
    result = {}
    for spec in specs:
        label, separator, name = spec.partition("=")
        if not separator or not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_-]*", label) or label in result:
            raise ValueError("Use a unique label=path for each --binary")
        path = Path(name).resolve()
        with path.open("rb") as stream:
            if stream.read(4) != b"\x7fELF":
                raise ValueError("Harbor task containers require a compatible Linux ELF binary")
        result[label] = {"path": str(path), "sha256": digest(path)}
    return result


def schedule(labels, tasks, rounds):
    entries = []
    for round_index in range(rounds):
        for task_index, task in enumerate(tasks):
            shift = (round_index + task_index) % len(labels)
            for label in labels[shift:] + labels[:shift]:
                entries.append({"round": round_index + 1, "task": task, "label": label,
                                "job": f"r{round_index + 1}-{task}-{label}"})
    return entries


def selected_profile(directory, model, model_key=None):
    directory = Path(directory).resolve()
    config = json.loads((directory / "models.json").read_text())
    key = model_key or config.get("default")
    matches = [entry for entry in config["models"] if entry.get("key") == key]
    if len(matches) != 1 or matches[0].get("id") != model:
        raise ValueError("The selected model key must identify exactly one entry matching --model")
    selected = dict(matches[0])
    credentials = []
    if selected.get("providerId") == "openai-codex" and not selected.get("apiKey"):
        connection = str(UUID(selected["connectionId"]))
        source = directory / "codex-auth" / (connection + ".json")
        if not source.is_file():
            raise ValueError("Selected Codex connection is missing its isolated credential file")
        credentials.append((source, "codex-auth/" + source.name))
    # The server resolves this against its own config directory. Never carry
    # a host-side credential path into the container.
    selected.pop("codexCredentialPath", None)
    return {"default": key, "models": [selected]}, credentials
