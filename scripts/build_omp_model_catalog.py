#!/usr/bin/env python3
"""Build Vista's compact snapshot from oh-my-pi's generated models.json.

OMP's committed models.json contains transport-specific fields that Vista
cannot consume directly.  This script keeps the stable model identity,
display name, capabilities, limits, and thinking policy while dropping runtime
telemetry, pricing, headers, and protocol compatibility details.

The input is intentionally explicit instead of fetched by the build.  A
release can therefore be reproduced from a reviewed OMP checkout and the
resulting snapshot remains available when the network is offline.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import sys
from typing import Any, NoReturn


ROOT = pathlib.Path(__file__).resolve().parent.parent
DEFAULT_OUTPUT = ROOT / "internal" / "provider" / "omp_models.json"
MAX_MODELS = 20000
MAX_PROVIDERS = 256
EFFORTS = {"minimal", "low", "medium", "high", "xhigh", "max"}
MODALITIES = {"text", "image", "audio", "video", "pdf"}


def fail(message: str) -> NoReturn:
    raise ValueError(message)


def string(value: Any, field: str, *, required: bool = False, limit: int = 512) -> str:
    if not isinstance(value, str):
        if required:
            fail(f"{field} must be a string")
        return ""
    value = value.strip()
    if required and not value:
        fail(f"{field} is required")
    if len(value) > limit:
        fail(f"{field} exceeds {limit} bytes")
    return value


def positive_int(value: Any, field: str, *, required: bool = False) -> int:
    # OMP uses null for an unknown limit. Vista uses zero for the same state.
    if value is None:
        return 0
    if not isinstance(value, int) or isinstance(value, bool) or value < 0:
        if required:
            fail(f"{field} must be a non-negative integer")
        return 0
    return value


def model_row(provider: str, model_id: str, raw: Any) -> dict[str, Any]:
    if not isinstance(raw, dict):
        fail(f"{provider}/{model_id} is not an object")
    if string(raw.get("id"), f"{provider}/{model_id}.id", required=True, limit=512) != model_id:
        fail(f"{provider}/{model_id}.id does not match its key")
    if string(raw.get("provider"), f"{provider}/{model_id}.provider", required=True, limit=128) != provider:
        fail(f"{provider}/{model_id}.provider does not match its key")
    api = string(raw.get("api"), f"{provider}/{model_id}.api", required=True, limit=128)
    name = string(raw.get("name"), f"{provider}/{model_id}.name", required=True)
    inputs = raw.get("input")
    if not isinstance(inputs, list) or not inputs or any(not isinstance(item, str) or item not in MODALITIES for item in inputs):
        fail(f"{provider}/{model_id}.input is invalid")
    if len(set(inputs)) != len(inputs):
        fail(f"{provider}/{model_id}.input contains duplicates")

    result: dict[str, Any] = {
        "name": name,
        "api": api,
        "reasoning": bool(raw.get("reasoning")),
        "input": inputs,
        "contextWindow": positive_int(raw.get("contextWindow"), f"{provider}/{model_id}.contextWindow", required=True),
        "maxTokens": positive_int(raw.get("maxTokens"), f"{provider}/{model_id}.maxTokens", required=True),
    }
    if "supportsTools" in raw:
        if not isinstance(raw["supportsTools"], bool):
            fail(f"{provider}/{model_id}.supportsTools is invalid")
        result["supportsTools"] = raw["supportsTools"]

    thinking = raw.get("thinking")
    if thinking is not None:
        if not isinstance(thinking, dict):
            fail(f"{provider}/{model_id}.thinking is invalid")
        mode = string(thinking.get("mode"), f"{provider}/{model_id}.thinking.mode", required=True, limit=64)
        efforts = thinking.get("efforts")
        if not isinstance(efforts, list) or not efforts or any(not isinstance(effort, str) or effort not in EFFORTS for effort in efforts):
            fail(f"{provider}/{model_id}.thinking.efforts is invalid")
        if len(set(efforts)) != len(efforts):
            fail(f"{provider}/{model_id}.thinking.efforts contains duplicates")
        policy: dict[str, Any] = {"mode": mode, "efforts": efforts}
        default_level = thinking.get("defaultLevel")
        if default_level is not None:
            if not isinstance(default_level, str) or default_level not in efforts:
                fail(f"{provider}/{model_id}.thinking.defaultLevel is invalid")
            policy["defaultLevel"] = default_level
        effort_map = thinking.get("effortMap")
        if effort_map is not None:
            if not isinstance(effort_map, dict):
                fail(f"{provider}/{model_id}.thinking.effortMap is invalid")
            mapped: dict[str, str] = {}
            for effort, wire_value in effort_map.items():
                if effort not in efforts or not isinstance(wire_value, str) or not wire_value or len(wire_value) > 64:
                    fail(f"{provider}/{model_id}.thinking.effortMap is invalid")
                mapped[effort] = wire_value
            if mapped:
                policy["effortMap"] = mapped
        for boolean_field in ("supportsDisplay", "requiresEffort"):
            if boolean_field in thinking:
                if not isinstance(thinking[boolean_field], bool):
                    fail(f"{provider}/{model_id}.thinking.{boolean_field} is invalid")
                if thinking[boolean_field]:
                    policy[boolean_field] = True
        result["thinking"] = policy
    return result


def build(payload: Any, revision: str) -> dict[str, Any]:
    if not isinstance(payload, dict) or not payload:
        fail("OMP models.json must be a non-empty object")
    if len(payload) > MAX_PROVIDERS:
        fail("OMP models.json contains too many providers")
    providers: dict[str, dict[str, dict[str, Any]]] = {}
    count = 0
    for provider, models in sorted(payload.items()):
        provider = string(provider, "provider", required=True, limit=128)
        if not isinstance(models, dict):
            fail(f"{provider} must contain a model object")
        rows: dict[str, dict[str, Any]] = {}
        for model_id, raw in sorted(models.items()):
            model_id = string(model_id, f"{provider} model id", required=True, limit=512)
            if model_id in rows:
                fail(f"duplicate OMP model {provider}/{model_id}")
            rows[model_id] = model_row(provider, model_id, raw)
            count += 1
        if rows:
            providers[provider] = rows
    if count == 0 or count > MAX_MODELS:
        fail("OMP models.json contains an invalid number of models")
    return {
        "source": "oh-my-pi",
        "revision": revision,
        "modelCount": count,
        "providers": providers,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", type=pathlib.Path, required=True, help="OMP packages/catalog/src/models.json")
    parser.add_argument("--output", type=pathlib.Path, default=DEFAULT_OUTPUT)
    parser.add_argument("--revision", default="unknown", help="reviewed OMP git revision")
    args = parser.parse_args()
    payload = json.loads(args.input.read_text(encoding="utf-8"))
    result = build(payload, args.revision)
    encoded = json.dumps(result, ensure_ascii=False, sort_keys=True, separators=(",", ":")) + "\n"
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(encoded, encoding="utf-8")
    digest = hashlib.sha256(encoded.encode()).hexdigest()
    print(f"Saved {result['modelCount']} OMP models to {args.output}; snapshot SHA-256 {digest}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"build_omp_model_catalog.py: {exc}", file=sys.stderr)
        raise SystemExit(1)
