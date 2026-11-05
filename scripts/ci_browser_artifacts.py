#!/usr/bin/env python3
"""Collect only failed browser tests' diagnostic attachments for CI upload."""

import argparse
import json
from pathlib import Path
import shutil


ROOT = Path(__file__).resolve().parent.parent


def collect(suite, source, output):
    count = 0
    for spec in suite.get("specs", []):
        for test in spec.get("tests", []):
            for result in test.get("results", []):
                if result.get("status") not in {"failed", "timedOut", "interrupted"}:
                    continue
                for attachment in result.get("attachments", []):
                    if attachment.get("name") not in {"trace", "screenshot", "error-context"} or not attachment.get("path"):
                        continue
                    path = Path(attachment["path"]).resolve()
                    relative = path.relative_to(source)
                    destination = output / relative
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copyfile(path, destination)
                    count += 1
    return count + sum(collect(child, source, output) for child in suite.get("suites", []))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", type=Path, default=ROOT / "web/.vista/test-state/browser-report.json")
    parser.add_argument("--source", type=Path, default=ROOT / "web/.vista/test-state/e2e-results")
    parser.add_argument("--output", type=Path, default=ROOT / ".vista/ci/browser-failures")
    args = parser.parse_args()
    if not args.report.exists():
        print("No browser report was produced; no attachments to collect.")
        return
    count = collect(json.loads(args.report.read_text(encoding="utf-8")), args.source.resolve(), args.output.resolve())
    print(f"Collected {count} failed-test attachments.")


if __name__ == "__main__":
    main()
