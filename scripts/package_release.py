#!/usr/bin/env python3
"""Package Vista and the optional native desktop shell with their notices."""

import argparse
import gzip
import hashlib
import io
import os
import plistlib
from pathlib import Path
import re
import subprocess
import tarfile
import zipfile


ROOT = Path(__file__).resolve().parent.parent


def metadata(binary):
    return subprocess.check_output(
        ["go", "version", "-m", str(binary.resolve())],
        env=dict(os.environ, GOWORK="off"), text=True,
    )


def settings(info):
    return dict(re.findall(r"^\s*build\s+(\S+)=(.*?)\s*$", info, re.M))


def desktop_location(system):
    return {"windows": "vista-desktop.exe", "linux": "vista-desktop",
            "darwin": "Vista.app/Contents/MacOS/vista-desktop"}[system]


def desktop_entries(desktop, service, system, version):
    location = desktop_location(system)
    entries = {location: desktop.read_bytes()}
    if system == "linux":
        entries["share/applications/io.github.willvar.VistaDesktop.desktop"] = (
            ROOT / "desktop/vista.desktop").read_bytes()
        entries["share/icons/hicolor/64x64/apps/io.github.willvar.VistaDesktop.png"] = (
            ROOT / "desktop/vista.png").read_bytes()
    elif system == "darwin":
        entries["Vista.app/Contents/MacOS/vista"] = service
        short_version = version.removeprefix("v") if re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", version) else "0.0.0"
        entries["Vista.app/Contents/Info.plist"] = plistlib.dumps({
            "CFBundleIdentifier": "io.github.willvar.VistaDesktop",
            "CFBundleExecutable": "vista-desktop",
            "CFBundleName": "Vista",
            "CFBundlePackageType": "APPL",
            "CFBundleShortVersionString": short_version,
            "CFBundleVersion": short_version,
            "NSHighResolutionCapable": True,
            "NSAppTransportSecurity": {"NSAllowsLocalNetworking": True},
        })
    return entries


def executable_entry(name):
    return Path(name).name in ("vista", "vista.exe", "vista-desktop", "vista-desktop.exe")


def package(binary: Path, version: str, output: Path, desktop: Path | None = None) -> Path:
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.+_-]*", version):
        raise ValueError("invalid package version")
    data = binary.read_bytes()
    notices = (ROOT / "licenses/NOTICE").read_bytes()
    if notices not in data:
        raise ValueError("binary does not contain the current license notices; rebuild it first")
    target = settings(metadata(binary))
    target_os, arch = target["GOOS"], target["GOARCH"]
    binary_name = "vista.exe" if target_os == "windows" else "vista"
    entries = {
        binary_name: data,
        "LICENSE": (ROOT / "LICENSE").read_bytes(),
        "THIRD_PARTY_NOTICES": notices,
    }
    if desktop is not None:
        info = metadata(desktop)
        companion = settings(info)
        keys = ("GOOS", "GOARCH", "vcs.revision", "vcs.modified")
        if (target_os not in ("linux", "darwin", "windows") or not target.get("vcs.revision")
                or any(key not in target or target[key] != companion.get(key) for key in keys)
                or not re.search(r"^\s*path\s+vista/desktop\s*$", info, re.M)):
            raise ValueError("desktop shell must match the service's source and target")
        entries.update(desktop_entries(desktop, data, target_os, version))
    output.mkdir(parents=True, exist_ok=True)
    archive = output / f"vista_{version}_{target_os}_{arch}{'.zip' if target_os == 'windows' else '.tar.gz'}"
    if target_os == "windows":
        with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as package:
            for name, content in entries.items():
                info = zipfile.ZipInfo(name, (1980, 1, 1, 0, 0, 0))
                info.create_system = 3
                info.external_attr = (0o100755 if name.endswith(".exe") else 0o100644) << 16
                info.compress_type = zipfile.ZIP_DEFLATED
                package.writestr(info, content)
    else:
        with archive.open("wb") as raw:
            with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0, compresslevel=9) as compressed:
                with tarfile.open(fileobj=compressed, mode="w", format=tarfile.GNU_FORMAT) as package:
                    for name, content in entries.items():
                        info = tarfile.TarInfo(name)
                        info.size = len(content)
                        info.mode = 0o755 if executable_entry(name) else 0o644
                        info.mtime = 0
                        package.addfile(info, io.BytesIO(content))
    archives = sorted([*output.glob("vista_*.zip"), *output.glob("vista_*.tar.gz")])
    (output / "SHA256SUMS").write_text(
        "".join(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n" for path in archives),
        encoding="utf-8",
    )
    return archive


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--desktop", type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    print(package(args.binary, args.version, args.output_dir, args.desktop))
