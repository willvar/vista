#!/usr/bin/env python3
"""Check the 8 GiB, 20-Session mixed backend load for late memory growth."""

import argparse
import json
import math
import statistics
import sys
from pathlib import Path


LIMITS_MIB = {
    "serverRSSMiB": 16,
    "workerRSSMiB": 32,
    "workerHeapMiB": 12,
    "cgroup.anonMiB": 32,
}


def metric(checkpoint, name):
    if name.startswith("cgroup."):
        value = checkpoint["cgroup"][name.split(".", 1)[1]]
    else:
        value = checkpoint[name]
    if not isinstance(value, (int, float)) or not math.isfinite(value) or value < 0:
        raise ValueError(f"invalid {name} at round {checkpoint.get('round')}")
    return value


def evaluate(result, minimum_rounds, minimum_closed_sessions):
    rounds = result.get("rounds")
    checkpoints = result.get("checkpoints", [])
    if minimum_rounds < 42 or not isinstance(rounds, int) or rounds < minimum_rounds or len(checkpoints) != rounds:
        raise ValueError(f"expected at least {minimum_rounds} complete rounds")
    for index, point in enumerate(checkpoints, 1):
        if point.get("round") != index:
            raise ValueError(f"missing or unordered checkpoint at round {index}")
        cgroup = point.get("cgroup")
        if not isinstance(cgroup, dict) or cgroup.get("maxMiB") != 8192:
            raise ValueError(f"round {index} was not in an 8 GiB cgroup")
        if cgroup.get("oom", 0) or cgroup.get("oomKill", 0):
            raise ValueError(f"cgroup OOM at round {index}")
        groups = point.get("groups", {})
        if not isinstance(groups, dict) or any(not isinstance(groups.get(name), dict) for name in ("shell", "mcp", "delegate")):
            raise ValueError(f"round {index} has invalid Worker groups")
        if [groups.get(name, {}).get("count") for name in ("shell", "mcp", "delegate")] != [8, 6, 6]:
            raise ValueError(f"round {index} lost a Worker group")
    sessions = result.get("sessions")
    if not isinstance(sessions, dict) or sessions.get("open") != 20 or sessions.get("workers") != 20 or sessions.get("running") != 0 or sessions.get("unavailable") != 0:
        raise ValueError("final 20-Session state is incomplete")
    closed = result.get("closedSessionCount")
    closed_verified = isinstance(closed, int) and not isinstance(closed, bool) and closed > 0 and closed >= minimum_closed_sessions and sessions.get("closed") == closed and sessions.get("total") == 20 + closed
    if minimum_closed_sessions and not closed_verified:
        raise ValueError("closed Session records or Worker release checks are incomplete")
    calls = result.get("calls")
    if not isinstance(calls, dict) or any(calls.get(name) != factor * rounds for name, factor in (("shell", 16), ("mcp", 18), ("delegate", 18), ("child", 6))):
        raise ValueError("mixed tool call totals are incomplete")
    worker_pids = result.get("workerPIDs")
    identity_verified = (
        isinstance(worker_pids, dict)
        and len(worker_pids) == 20
        and len(set(worker_pids.values())) == 20
        and all(isinstance(pid, int) and pid > 0 for pid in worker_pids.values())
        and result.get("workerIdentityChecks") == rounds + 1
    )
    if not identity_verified:
        raise ValueError("Worker identity checks are missing or incomplete")
    final_cgroup = result.get("cgroup")
    if not isinstance(final_cgroup, dict) or final_cgroup.get("maxMiB") != 8192 or final_cgroup.get("oom", 0) or final_cgroup.get("oomKill", 0):
        raise ValueError("final cgroup limit or OOM state is invalid")
    peak = metric({"round": rounds, "cgroup": final_cgroup}, "cgroup.peakMiB")
    if peak > 6144:
        raise ValueError(f"cgroup peak {peak} MiB leaves less than 2 GiB of the 8 GiB budget")

    # Compare idle checkpoints after half the work has finished with the final
    # 21 rounds. Medians dampen one-off GC and RSS sampling noise. File cache
    # growth is covered by the peak gate, while anon and live heap show whether
    # Vista's long-lived allocations keep growing after warmup.
    warm = checkpoints[rounds // 2 - 1 : rounds // 2 + 20]
    late = checkpoints[-21:]
    growth = {}
    for name, limit in LIMITS_MIB.items():
        initial = statistics.median(metric(point, name) for point in warm)
        final = statistics.median(metric(point, name) for point in late)
        delta = round(final - initial, 1)
        growth[name] = {"warmMiB": initial, "lateMiB": final, "growthMiB": delta, "limitMiB": limit}
        if delta > limit:
            raise ValueError(f"{name} grew {delta} MiB after warmup; limit is {limit} MiB")
    return {"rounds": rounds, "cgroupPeakMiB": peak, "workerIdentityVerified": identity_verified, "closedSessionsVerified": closed_verified, "growth": growth}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("result", type=Path, help="JSON output from mixed_capacity_smoke.mjs")
    parser.add_argument("--minimum-rounds", type=int, default=240)
    parser.add_argument("--minimum-closed-sessions", type=int, default=0, help="require closed Session records to coexist with the 20 open Workers")
    args = parser.parse_args()
    try:
        if args.minimum_closed_sessions < 0:
            raise ValueError("minimum closed Session count must be nonnegative")
        result = json.loads(args.result.read_text())
        print(json.dumps(evaluate(result, args.minimum_rounds, args.minimum_closed_sessions), indent=2))
    except (OSError, ValueError, KeyError, TypeError, json.JSONDecodeError) as error:
        print(f"mixed capacity gate failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
