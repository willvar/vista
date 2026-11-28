#!/usr/bin/env python3
"""Build the native desktop shell without changing the Go module cache."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
from zipfile import ZipFile

DESKTOP = Path(__file__).resolve().parent
WAILS = "github.com/wailsapp/wails/v3"


def build(output, test=False, version="dev"):
    env = dict(os.environ, GOWORK="off", CGO_ENABLED="0")

    def go(*args, capture=False):
        return subprocess.run(["go", *args], cwd=DESKTOP, env=env, text=True, check=True,
                              stdout=subprocess.PIPE if capture else None).stdout

    platform = go("env", "GOOS", capture=True).strip()
    if platform not in ("linux", "windows", "darwin"):
        raise ValueError("desktop build supports Linux, macOS and Windows")
    output = output.resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    cache = DESKTOP.parent / ".vista" / "build"
    cache.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="desktop-", dir=cache) as temporary:
        staging = Path(temporary)
        binary = staging / output.name
        args = ["test", "-c"] if test else ["build"]
        args += ["-mod=readonly", "-trimpath", "-o", str(binary)]
        link = ["-X vista/internal/buildinfo.Version=" + version]
        if platform == "linux":
            args += ["-tags", "gtk3,purego,production"]
        elif platform == "darwin":
            args += ["-tags", "purego,production"]
        else:
            source = json.loads(go("mod", "download", "-json", WAILS, capture=True))
            wails_version = source["Version"]
            if f"{WAILS} {wails_version} {source['Sum']}" not in (DESKTOP / "go.sum").read_text().splitlines():
                raise ValueError("Wails module does not match the pinned checksum")
            with ZipFile(source["Zip"]) as package:
                package.extractall(staging)
            module = staging / f"{WAILS}@{wails_version}"
            diff = DESKTOP / f"wails-{wails_version.removeprefix('v')}-windows.patch"
            # A pinned delta for the public navigation and tray hooks, never an
            # edited shared dependency or a fork fetched from an unpinned ref.
            subprocess.run(["git", "apply", "--unidiff-zero", str(diff)], cwd=module,
                           env=dict(env, GIT_CEILING_DIRECTORIES=str(module.parent)), check=True)
            modfile = staging / "desktop.mod"
            modfile.write_bytes((DESKTOP / "go.mod").read_bytes())
            modfile.with_suffix(".sum").write_bytes((DESKTOP / "go.sum").read_bytes())
            go("mod", "edit", f"-modfile={modfile}", f"-replace={WAILS}={module}", f"-replace=vista={DESKTOP.parent}")
            args += [f"-modfile={modfile}", "-tags", "production"]
            # Keep normal terminal output; the launcher hides its own console
            # when Explorer, rather than a terminal, starts the app.
        go(*args, "-ldflags=" + " ".join(link), ".")
        binary.replace(output)


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--output", type=Path, required=True)
    cli.add_argument("--test", action="store_true", help="build the native desktop test executable")
    cli.add_argument("--version", default="dev")
    args = cli.parse_args()
    build(args.output, args.test, args.version)
