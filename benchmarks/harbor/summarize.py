"""Summarize original Harbor rewards; keep missing and failed trials visible."""

import argparse
from collections import defaultdict
from datetime import datetime
import json
from pathlib import Path
import statistics


def read_json(path, default=None):
    return json.loads(path.read_text()) if path.is_file() else default


def vista_usage(path):
    requested = completed = missing = 0
    models, seen = {}, set()
    malformed = False
    if path.is_file():
        for line in path.read_text().splitlines():
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                malformed = True
                continue
            if event["sequence"] in seen:
                continue
            seen.add(event["sequence"])
            if event["kind"] == "model_request":
                requested += 1
            if event["kind"] != "model_result":
                continue
            completed += 1
            result = event["data"]["modelResult"]
            usage = result.get("usage")
            if usage is None or "inputTokens" not in usage or "outputTokens" not in usage:
                missing += 1
                continue
            total = models.setdefault(result["provider"] + "/" + result["id"], dict(n_input_tokens=0, n_output_tokens=0, n_cache_tokens=0))
            total["n_input_tokens"] += usage["inputTokens"]
            total["n_output_tokens"] += usage["outputTokens"]
            total["n_cache_tokens"] += usage.get("cacheReadTokens", 0)
    complete = requested > 0 and requested == completed and not missing and not malformed
    return {"complete": complete, "model_requests": requested, "completed_model_requests": completed,
            "missing_usage": missing, "incomplete_trace": malformed, "models": models if complete else {}}


def elapsed(timing):
    if not timing or not timing.get("started_at") or not timing.get("finished_at"):
        return None
    seconds = (datetime.fromisoformat(timing["finished_at"]) - datetime.fromisoformat(timing["started_at"])).total_seconds()
    return seconds if seconds >= 0 else None


def trial(job, entry):
    results = [path for path in job.glob("*/result.json") if read_json(path, {}).get("task_name")]
    if len(results) != 1:
        return {**entry, "state": "missing_result" if not results else "ambiguous_results", "reward": None,
                "success": False, "agent_seconds": None, "tokens": None}
    result = read_json(results[0])
    agent_dir = results[0].parent / "agent"
    runtime = read_json(agent_dir / "vista-summary.json", {})
    finish = runtime.get("run_result", {})
    exception = result.get("exception_info")
    reward = ((result.get("verifier_result") or {}).get("rewards") or {}).get("reward")
    normal = (finish.get("status") == "succeeded" and not finish.get("pendingReply")
              and not exception and not runtime.get("runner_error"))
    state = "error" if exception or runtime.get("runner_error") else "passed" if normal and reward == 1 else "failed" if reward is not None else "unjudged"
    usage = vista_usage(agent_dir / "vista-events.jsonl")
    tokens = {field: sum(value[field] for value in usage["models"].values())
              for field in ("n_input_tokens", "n_output_tokens", "n_cache_tokens")} if usage["complete"] else None
    return {**entry, "state": state, "reward": reward, "success": normal and reward == 1,
            "runtime_status": finish.get("status"), "exception_type": exception.get("exception_type") if exception else None,
            "agent_seconds": elapsed(result.get("agent_execution")), "wall_seconds": elapsed(result),
            "tokens": tokens, "cost_usd": (result.get("agent_result") or {}).get("cost_usd"),
            "model_requests": usage["model_requests"], "usage_complete": usage["complete"],
            "build": runtime.get("build"), "result": str(results[0])}


def level_totals(records):
    levels = {}
    for level in sorted({row["level"] for row in records if "level" in row}):
        rows = [row for row in records if row.get("level") == level]
        levels[str(level)] = {"planned": len(rows), "successes": sum(row["success"] for row in rows),
                              "states": {state: sum(row["state"] == state for row in rows) for state in sorted({row["state"] for row in rows})}}
    return levels


def summarize_run(directory):
    plan = read_json(directory / "plan.json")
    records = [trial(directory / "jobs" / entry["job"], entry) for entry in plan["sequence"]]
    groups = defaultdict(list)
    for record in records:
        groups[record["label"]].append(record)
    totals = {}
    for label, selected in groups.items():
        times = [row["agent_seconds"] for row in selected if row["success"] and row["agent_seconds"] is not None]
        totals[label] = {"planned": len(selected), "successes": sum(row["success"] for row in selected),
                         "states": {state: sum(row["state"] == state for row in selected) for state in sorted({row["state"] for row in selected})},
                         "successful_agent_seconds": {"n": len(times), "median": statistics.median(times) if times else None,
                                                      "min": min(times, default=None), "max": max(times, default=None)}}
        if levels := level_totals(selected):
            totals[label]["levels"] = levels
    return {"suite": plan.get("suite", "harbor"), "dataset": plan["dataset"], "dataset_commit": plan["dataset_commit"], "model": plan["model"],
            "reasoning_effort": plan["reasoning_effort"], "binaries": plan["binaries"], "totals": totals,
            "levels": level_totals(records), "trials": records,
            "scope": plan.get("scope", "Fixed exploratory sample; not an estimate of overall task success rate. Timing aggregates include successful autonomous completions only.")}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("run", type=Path)
    args = parser.parse_args()
    print(json.dumps(summarize_run(args.run.resolve()), indent=2))
