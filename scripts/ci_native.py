#!/usr/bin/env python3
"""Build and exercise a native validation archive without publishing a release."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import zipfile


ROOT = Path(__file__).resolve().parent.parent


def sha256(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def run(command, env):
    print("+ " + " ".join(map(str, command)), flush=True)
    return subprocess.check_output(command, cwd=ROOT, env=env, text=True, stderr=subprocess.STDOUT)


def unpack(archive, binary, output):
    expected = {
        binary.name: binary.read_bytes(),
        "LICENSE": (ROOT / "LICENSE").read_bytes(),
        "THIRD_PARTY_NOTICES": (ROOT / "licenses/NOTICE").read_bytes(),
    }
    if archive.suffix == ".zip":
        with zipfile.ZipFile(archive) as package:
            if set(package.namelist()) != set(expected):
                raise ValueError("unexpected archive contents")
            actual = {name: package.read(name) for name in expected}
    else:
        with tarfile.open(archive) as package:
            if set(package.getnames()) != set(expected):
                raise ValueError("unexpected archive contents")
            actual = {name: package.extractfile(name).read() for name in expected}
            if package.getmember(binary.name).mode != 0o755:
                raise ValueError("archive executable permissions are incorrect")
    if actual != expected:
        raise ValueError("archive contents differ from the built binary or current notices")
    output.mkdir(parents=True, exist_ok=True)
    for name, contents in actual.items():
        path = output / name
        path.write_bytes(contents)
        path.chmod(0o755 if name == binary.name else 0o644)
    return output / binary.name


def native_tests(binary, output, env):
    env = dict(env, VISTA_TEST_BINARY=str(binary))
    command = ["go", "test", "-json", "-count=1", "-timeout=5m",
               "./cmd/vista", "./internal/platform", "./internal/mcphost"]
    print("+ " + " ".join(command), flush=True)
    passed, skipped, latest_output = set(), [], {}
    with (output / "native-tests.jsonl").open("w", encoding="utf-8") as log:
        process = subprocess.Popen(command, cwd=ROOT, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, text=True, encoding="utf-8")
        for line in process.stdout:
            log.write(line)
            log.flush()
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                print(line.rstrip(), flush=True)
                continue
            test, action = event.get("Test"), event.get("Action")
            key = (event.get("Package"), test)
            text = event.get("Output", "").strip()
            if test and text and not text.startswith(("===", "---")):
                latest_output[key] = text[-2000:]
            if action == "pass" and test:
                passed.add(key)
            elif action == "skip" and test:
                skipped.append({"package": event["Package"], "test": test, "reason": latest_output.get(key, "")})
                print("Skipped: " + event["Package"] + "/" + test, flush=True)
            elif action == "fail" or (action == "pass" and not test):
                print(json.dumps(event), flush=True)
        exit_code = process.wait()
    if exit_code:
        print((output / "native-tests.jsonl").read_text(encoding="utf-8")[-24000:], flush=True)
        raise subprocess.CalledProcessError(exit_code, command)
    required = {("vista/cmd/vista", name) for name in ("TestNativeApplication", "TestNativeResetPort")}
    required |= {("vista/internal/platform", name) for name in (
        "TestNativeFileLock", "TestNativeMemory", "TestNativeOwnedSession", "TestNativeShell", "TestNativeShellErrorsRemainText")}
    if sys.platform == "win32":
        required |= {("vista/cmd/vista", name) for name in ("TestNativeWindowsDesktopLauncher", "TestNativeWindowsTrayLongStatus")}
    elif sys.platform == "darwin":
        required.add(("vista/internal/platform", "TestNativeReapedOwnedSession"))
    if missing := required - passed:
        raise ValueError("mandatory native tests did not pass: " + ", ".join("/".join(key) for key in sorted(missing)))
    return {"passed": len(passed), "skipped": skipped, "required_passed": ["/".join(key) for key in sorted(required)]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", required=True, choices=["linux/amd64", "windows/amd64", "darwin/amd64", "darwin/arm64"])
    args = parser.parse_args()
    env = {key: value for key, value in os.environ.items() if not key.startswith("VISTA_")}
    env.update(GOWORK="off", CGO_ENABLED="0", VISTA_NO_BROWSER="1",
               VISTA_WEBSEARCH_EXA_ENDPOINT="http://127.0.0.1:1/exa",
               VISTA_WEBSEARCH_FIRECRAWL_ENDPOINT="http://127.0.0.1:1/firecrawl",
               VISTA_WEBSEARCH_PARALLEL_ENDPOINT="http://127.0.0.1:1/parallel")
    go = json.loads(run(["go", "env", "-json", "GOOS", "GOARCH", "GOHOSTOS", "GOHOSTARCH", "GOVERSION"], env))
    if args.target != go["GOOS"] + "/" + go["GOARCH"] or args.target != go["GOHOSTOS"] + "/" + go["GOHOSTARCH"]:
        raise ValueError("target must match the native Go host: " + json.dumps(go))
    source = run(["git", "rev-parse", "HEAD"], env).strip()
    dirty = bool(run(["git", "status", "--porcelain"], env).strip())
    if os.environ.get("GITHUB_ACTIONS") == "true" and (dirty or source != os.environ["GITHUB_SHA"]):
        raise ValueError("runner source must be clean and match the dispatched commit")
    output = ROOT / ".vista/ci/native" / args.target.replace("/", "_")
    output.mkdir(parents=True, exist_ok=True)
    report = {"source_commit": source, "source_modified": dirty, "version": "dev",
              "target": args.target, "go": go, "runner_os": os.environ.get("RUNNER_OS"),
              "runner_arch": os.environ.get("RUNNER_ARCH"), "status": "running"}
    try:
        binary = output / ("vista.exe" if go["GOOS"] == "windows" else "vista")
        run(["go", "build", "-trimpath", "-ldflags=-s -w -X vista/internal/buildinfo.Version=dev",
             "-o", str(binary), "./cmd/vista"], env)
        metadata = run(["go", "version", "-m", str(binary)], env)
        settings = dict(re.findall(r"^\s*build\s+(\S+)=(.*)$", metadata, re.M))
        for key, expected in {"GOOS": go["GOOS"], "GOARCH": go["GOARCH"],
                              "vcs.revision": source, "vcs.modified": str(dirty).lower()}.items():
            if settings.get(key) != expected:
                raise ValueError("binary metadata mismatch for " + key)
        archive = Path(run([sys.executable, "scripts/package_release.py", "--binary", str(binary),
                            "--version", "dev", "--output-dir", str(output / "dist")], env).strip())
        report.update(binary_sha256=sha256(binary), archive=archive.name, archive_sha256=sha256(archive),
                      archive_size=archive.stat().st_size, build_metadata=metadata)
        expected_checksum = report["archive_sha256"] + "  " + archive.name + "\n"
        if (output / "dist/SHA256SUMS").read_text(encoding="utf-8") != expected_checksum:
            raise ValueError("archive checksum file does not match")
        packed_binary = unpack(archive, binary, output / "unpacked")
        report["tests"] = native_tests(packed_binary, output, env)
        report["status"] = "passed"
    except Exception as error:
        report.update(status="failed", error=str(error))
        if isinstance(error, subprocess.CalledProcessError) and error.output:
            print(error.output, flush=True)
        raise
    finally:
        (output / "report.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
            with open(summary, "a", encoding="utf-8") as stream:
                stream.write(f"### Native {args.target}: {report['status']}\n\n")
                stream.write(f"Source: `{source}`; Go: `{go['GOVERSION']}`.\n\n")
                if tests := report.get("tests"):
                    stream.write(f"Passed: {tests['passed']}; skipped: {len(tests['skipped'])}.\n\n")
                    for skipped in tests["skipped"]:
                        stream.write(f"- Skipped: `{skipped['package']}/{skipped['test']}`: {skipped['reason']}\n")
                stream.write("\nValidation archives only; no release was published.\n")
        print(json.dumps(report, indent=2), flush=True)


if __name__ == "__main__":
    main()
