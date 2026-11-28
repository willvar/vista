#!/usr/bin/env python3
"""Build and exercise a native validation archive without publishing a release."""

import argparse
from contextlib import ExitStack
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import tempfile
import zipfile

from package_release import archive_entries, executable_entry, executable_location, settings


ROOT = Path(__file__).resolve().parent.parent
TARGETS = ("linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64", "darwin/amd64", "darwin/arm64")


def build_version(value):
    if value != "dev" and not re.fullmatch(r"v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)", value):
        raise argparse.ArgumentTypeError("version must be dev or vMAJOR.MINOR.PATCH")
    return value


def sha256(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def run(command, env):
    print("+ " + " ".join(map(str, command)), flush=True)
    return subprocess.check_output(command, cwd=ROOT, env=env, text=True, stderr=subprocess.STDOUT)


def verify_macos_bundle(bundle, env):
    subprocess.run(["codesign", "--verify", "--strict", "--deep", str(bundle)], env=env, check=True, timeout=30)
    subprocess.run(["xcrun", "stapler", "validate", str(bundle)], env=env, check=True, timeout=45)
    subprocess.run(["spctl", "--assess", "--type", "execute", str(bundle)], env=env, check=True, timeout=45)


def sign_macos(binary, version, stage, identity, profile, env):
    # Both credentials must be available in the release runner's keychain.
    # No key material is supplied by the repository or logged by this script.
    for name, content in archive_entries(binary, version, "darwin").items():
        if name.startswith("Vista.app/"):
            target = stage / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(content)
            target.chmod(0o755 if executable_entry(name) else 0o644)
    bundle = stage / "Vista.app"
    subprocess.run(["codesign", "--sign", identity, "--options", "runtime", "--timestamp", str(bundle)],
                   env=env, check=True, timeout=120)
    submission = stage / "notary.zip"
    subprocess.run(["ditto", "-c", "-k", "--keepParent", str(bundle), str(submission)],
                   env=env, check=True, timeout=60)
    result = subprocess.check_output(["xcrun", "notarytool", "submit", str(submission), "--keychain-profile", profile,
                                      "--wait", "--timeout", "12m", "--output-format", "json"],
                                     env=env, text=True, timeout=750)
    if json.loads(result).get("status") != "Accepted":
        raise ValueError("Apple did not accept the macOS desktop notarization")
    subprocess.run(["xcrun", "stapler", "staple", str(bundle)], env=env, check=True, timeout=90)
    return bundle


def unpack(archive, binary, output, system, version="dev", bundle=None):
    expected = archive_entries(binary, version, system, bundle)
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
            if any(package.getmember(name).mode != (0o755 if executable_entry(name) else 0o644)
                   for name in expected):
                raise ValueError("archive executable permissions are incorrect")
    if actual != expected:
        raise ValueError("archive contents differ from the built binary or current notices")
    output.mkdir(parents=True, exist_ok=True)
    for name, contents in actual.items():
        path = output / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(contents)
        path.chmod(0o755 if executable_entry(name) else 0o644)
    return output / executable_location(system)


def native_tests(binary, output, env):
    env = dict(env, VISTA_TEST_BINARY=str(binary))
    command = ["go", "test", "-json", "-count=1", "-timeout=5m",
               "./internal/launch", "./internal/platform", "./internal/mcphost"]
    log_path = output / "native-tests.jsonl"
    print("+ " + " ".join(command), flush=True)
    passed, skipped, latest_output = set(), [], {}
    with log_path.open("w", encoding="utf-8") as log:
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
        print(log_path.read_text(encoding="utf-8")[-24000:], flush=True)
        raise subprocess.CalledProcessError(exit_code, command)
    required = {("vista/internal/launch", name) for name in ("TestNativeApplication", "TestNativeResetPort")}
    required |= {("vista/internal/platform", name) for name in (
        "TestNativeFileLock", "TestNativeMemory", "TestNativeOwnedSession", "TestNativeShell", "TestNativeShellErrorsRemainText")}
    if sys.platform == "win32":
        required.add(("vista/internal/launch", "TestNativeWindowsDesktopLauncher"))
    elif sys.platform == "darwin":
        required.add(("vista/internal/platform", "TestNativeReapedOwnedSession"))
    if missing := required - passed:
        raise ValueError("mandatory native tests did not pass: " + ", ".join("/".join(key) for key in sorted(missing)))
    return {"passed": len(passed), "skipped": skipped, "required_passed": ["/".join(key) for key in sorted(required)]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", required=True, choices=TARGETS)
    parser.add_argument("--version", type=build_version, default="dev")
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
    output = ROOT / ".vista/ci/native" / (args.target.replace("/", "_") + "_" + args.version)
    output.mkdir(parents=True, exist_ok=True)
    report = {"source_commit": source, "source_modified": dirty, "version": args.version,
              "target": args.target, "go": go, "runner_os": os.environ.get("RUNNER_OS"),
              "runner_arch": os.environ.get("RUNNER_ARCH"), "status": "running"}
    staging = ExitStack()
    try:
        signing = go["GOOS"] == "darwin" and args.version != "dev"
        identity = os.environ.get("VISTA_MACOS_SIGNING_IDENTITY", "")
        profile = os.environ.get("VISTA_MACOS_NOTARY_PROFILE", "")
        if signing and (not identity.startswith("Developer ID Application:") or not profile):
            raise ValueError("macOS releases need a configured Developer ID Application identity and notarization profile")
        if sys.platform == "win32":
            # Check the launch context before building. Both Actions and Task
            # Scheduler can put processes in Jobs that forbid real detachment.
            subprocess.run([sys.executable, "-c", "pass"], env=env, check=True,
                           creationflags=subprocess.CREATE_BREAKAWAY_FROM_JOB)
        system = go["GOOS"]

        binary = output / Path(executable_location(system)).name
        run([sys.executable, "desktop/build.py", "--output", str(binary), "--version", args.version], env)
        bundle = None
        if signing:
            stage = Path(staging.enter_context(tempfile.TemporaryDirectory(prefix="sign-", dir=output)))
            bundle = sign_macos(binary, args.version, stage, identity, profile, env)
            binary = stage / executable_location(system)
        info = run(["go", "version", "-m", str(binary)], env)
        build = settings(info)
        for key, expected in {"GOOS": system, "GOARCH": go["GOARCH"], "CGO_ENABLED": "0",
                              "vcs.revision": source, "vcs.modified": str(dirty).lower()}.items():
            if build.get(key) != expected:
                raise ValueError("Vista binary metadata mismatch for " + key)
        command = [sys.executable, "scripts/package_release.py", "--binary", str(binary),
                   "--version", args.version, "--output-dir", str(output / "dist")]
        if bundle is not None:
            command += ["--bundle", str(bundle)]
        archive = Path(run(command, env).strip())
        checksum = sha256(archive)
        if checksum + "  " + archive.name not in (output / "dist/SHA256SUMS").read_text(encoding="utf-8").splitlines():
            raise ValueError("archive checksum file does not match")
        report.update(archive=archive.name, archive_sha256=checksum, archive_size=archive.stat().st_size,
                      build_metadata=info, binary_sha256=sha256(binary), cgo_enabled=False)
        packed = unpack(archive, binary, output / "unpacked", system, args.version, bundle)
        if bundle is not None:
            verify_macos_bundle(packed.parents[2], env)
            report["distribution_verified"] = True

        # The same unpacked image must pass both service and native window tests.
        report["tests"] = native_tests(packed, output, env)
        test = output / ("vista.test.exe" if sys.platform == "win32" else "vista.test")
        run([sys.executable, "desktop/build.py", "--test", "--output", str(test)], env)
        test_env = dict(env, VISTA_TEST_DESKTOP=str(packed))
        platform_tests = {
            "windows": ["TestNativeWindowsTray", "TestWindowsLoginEntry"],
            "linux": ["TestNativeLinuxTray", "TestLinuxLoginSetting", "TestDesktopExecEscapesFieldCodes",
                      "TestNativeLinuxWindowRecovery", "TestNativeLinuxDesktopNoCGO"],
            "darwin": ["TestNativeDarwinDesktop", "TestNativeMacNavigation", "TestMacLoginSetting"],
        }[system]
        required = ["TestDesktopInstanceID", "TestLoginFileOwnsOnlyItsEntry", *platform_tests]
        pattern = "^(" + "|".join(required) + ")$"
        desktop_log = run([str(test), "-test.v", "-test.timeout=135s", "-test.run=" + pattern], test_env)
        (output / "desktop-test.log").write_text(desktop_log, encoding="utf-8")
        for name in required:
            if not re.search(r"^--- PASS: " + name + " ", desktop_log, re.M):
                raise ValueError("mandatory desktop test did not pass: " + name)
        report["tests"]["passed"] += len(required)
        report["tests"]["required_passed"].extend("vista/desktop/" + name for name in required)
        report["status"] = "passed"
    except Exception as error:
        report.update(status="failed", error=str(error))
        if isinstance(error, subprocess.CalledProcessError) and error.output:
            print(error.output, flush=True)
        raise
    finally:
        staging.close()
        (output / "report.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
            with open(summary, "a", encoding="utf-8") as stream:
                stream.write(f"### Native {args.target}: {report['status']}\n\n")
                stream.write(f"Source: `{source}`; Go: `{go['GOVERSION']}`.\n\n")
                if tests := report.get("tests"):
                    stream.write(f"Passed: {tests['passed']}; skipped: {len(tests['skipped'])}.\n\n")
                    for skipped in tests["skipped"]:
                        stream.write(f"- Skipped: `{skipped['package']}/{skipped['test']}`: {skipped['reason']}\n")
                stream.write("\nArchives validated; publication is a separate workflow job.\n")
        print(json.dumps(report, indent=2), flush=True)


if __name__ == "__main__":
    main()
