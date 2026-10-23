"""Select one model and its credential files without copying a user's profile."""

import hashlib
import json
from pathlib import Path
from uuid import UUID


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


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
