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


def profile_path(directory, section="models"):
    directory = Path(directory)
    unified = directory / "config.json"
    try:
        unified.lstat()
    except FileNotFoundError:
        return directory / (section + ".json")
    # Even an unreadable or dangling unified file must not revive legacy data.
    return unified


def _json_object(pairs):
    value = dict(pairs)
    if len(value) != len(pairs):
        raise ValueError("Configuration contains duplicate fields")
    return value


def _invalid_json_constant(_):
    raise ValueError("Configuration must contain only JSON values")


def profile_section(directory, section, model_key=None, source_paths=None):
    """Read portable selected data, resolving only the chosen model or search bundle."""
    path = profile_path(directory, section)
    try:
        config = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=_json_object,
                            parse_constant=_invalid_json_constant)
    except FileNotFoundError:
        if path.name == "config.json" or section != "search":
            raise
        return None
    if source_paths is not None:
        source_paths.append(path)
    version = None
    secret_fields = {"models": ("apiKey", "accessToken", "authToken", "appCode", "headers"),
                     "mcp": ("token", "clientSecret", "headers"),
                     "search": ("apiKeys", "jinaApiKey", "deepseekFetchApiKey")}
    if path.name == "config.json":
        if not isinstance(config, dict) or type(config.get("version")) is not int or config["version"] not in (1, 2):
            raise ValueError("Source config.json must have version 1 or 2")
        version = config["version"]
        if config.keys() - {"version", "server", "capacity", "agent", "search", "userAgent", "models", "mcp"}:
            raise ValueError("Source config.json contains unknown sections")
        if version == 2:
            # Validate the public boundary without looking up other records' keys.
            for managed, fields in secret_fields.items():
                public = config.get(managed)
                if managed == "models" and isinstance(public, dict):
                    arrays = [value for name, value in public.items() if name.casefold() == "models"]
                    if len(arrays) > 1:
                        raise ValueError("Configuration contains ambiguous model fields")
                    public = arrays[0] if arrays else None
                elif managed in ("models", "search") and public is not None and not isinstance(public, dict):
                    raise ValueError("Source config.json has an invalid credential section")
                records = [public] if managed == "search" else public
                if records is None:
                    continue
                if not isinstance(records, list) or any(entry is not None and not isinstance(entry, dict) for entry in records):
                    raise ValueError("Source config.json has an invalid credential collection")
                for entry in records:
                    if entry is None:
                        continue
                    if any(name.casefold() in {field.casefold() for field in fields} for name in entry):
                        raise ValueError("Source config.json must not contain inline static credentials")
                    refs = [value for name, value in entry.items() if name.casefold() == "credentialref"]
                    if len(refs) > 1 or (refs and (not isinstance(refs[0], str) or not refs[0])):
                        raise ValueError("Source config.json has an invalid credential reference")
        config = config.get(section)
    if config is None and section == "search":
        return None
    if not isinstance(config, dict):
        raise ValueError(f"Source profile has no valid {section} object")
    if section == "models":
        catalog = {}
        for name, value in config.items():
            canonical = name.casefold() if name.casefold() in ("models", "default") else name
            if canonical in catalog:
                raise ValueError("Configuration contains ambiguous model fields")
            catalog[canonical] = value
        if not isinstance(catalog.get("models"), list) or any(not isinstance(entry, dict) for entry in catalog["models"]):
            raise ValueError("Source models configuration must list model entries")
        key = model_key or catalog.get("default")
        names = ("key", "id", "connectionId", "providerId", "codexCredentialPath", "credentialRef", *secret_fields[section])
        canonical_names = {name.casefold(): name for name in names}
        matches = []
        for entry in catalog["models"]:
            normalized = {}
            for name, value in entry.items():
                canonical = canonical_names.get(name.casefold(), name)
                if canonical in normalized:
                    raise ValueError("Configuration contains ambiguous model fields")
                normalized[canonical] = value
            if normalized.get("key") == key:
                matches.append(normalized)
        if not isinstance(key, str) or not key or len(matches) != 1:
            raise ValueError("The selected model key must identify exactly one entry")
        record = matches[0]
        config = {**catalog, "default": key, "models": [record]}
        connection = record.get("connectionId")
        if connection is not None and not isinstance(connection, str):
            raise ValueError("Selected model has an invalid connection identity")
        owner = "connection:" + connection if connection else "key:" + key
    elif section == "search":
        canonical_names = {name.casefold(): name for name in ("credentialRef", *secret_fields[section])}
        record = {}
        for name, value in config.items():
            canonical = canonical_names.get(name.casefold(), name)
            if canonical in record:
                raise ValueError("Configuration contains ambiguous search fields")
            record[canonical] = value
        config, owner = record, "search"
    else:
        return config
    if "credentialRef" not in record:
        return config
    ref = record.pop("credentialRef")
    if version != 2 or not isinstance(ref, str) or not ref:
        raise ValueError("Static credential references require config.json version 2")
    key_path = Path(directory) / "key.json"
    keys = json.loads(key_path.read_text(encoding="utf-8"), object_pairs_hook=_json_object,
                      parse_constant=_invalid_json_constant)
    if (not isinstance(keys, dict) or set(keys) != {"version", "entries"}
            or type(keys["version"]) is not int or keys["version"] != 1 or not isinstance(keys["entries"], dict)):
        raise ValueError("Source key.json must have version 1 and an entries object")
    bundle = keys["entries"].get(ref)
    if (not isinstance(bundle, dict) or set(bundle) != {"section", "owner", "values"}
            or bundle["section"] != section or bundle["owner"] != owner or not isinstance(bundle["values"], dict)
            or bundle["values"].keys() - set(secret_fields[section])):
        raise ValueError("Selected credential reference is missing or does not match its owner")
    record.update(bundle["values"])
    if source_paths is not None:
        source_paths.append(key_path)
    return config


def selected_profile(directory, model, model_key=None, source_paths=None):
    directory = Path(directory).resolve()
    config = profile_section(directory, "models", model_key=model_key, source_paths=source_paths)
    selected = config["models"][0]
    if selected.get("id") != model:
        raise ValueError("The selected model key must identify exactly one entry matching --model")
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
    return {"default": config["default"], "models": [selected]}, credentials
