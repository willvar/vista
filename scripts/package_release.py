#!/usr/bin/env python3
"""Package one independently runnable Vista product with its notices."""

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


def executable_location(system):
    return {"windows": "vista.exe", "linux": "vista",
            "darwin": "Vista.app/Contents/MacOS/vista"}[system]


def archive_name(version, system, arch):
    return f"vista_{version}_{system}_{arch}" + (".zip" if system == "windows" else ".tar.gz")


def archive_entries(binary, version, system, bundle=None):
    data = binary.read_bytes()
    notices = (ROOT / "licenses/NOTICE").read_bytes()
    if notices not in data:
        raise ValueError("binary does not contain the current license notices; rebuild it first")
    entries = {
        executable_location(system): data,
        "LICENSE": (ROOT / "LICENSE").read_bytes(),
        "THIRD_PARTY_NOTICES": notices,
    }
    if system == "linux":
        entries["share/applications/io.github.willvar.VistaDesktop.desktop"] = (
            ROOT / "desktop/vista.desktop").read_bytes()
        entries["share/icons/hicolor/64x64/apps/io.github.willvar.VistaDesktop.png"] = (
            ROOT / "desktop/vista.png").read_bytes()
    elif system == "darwin" and bundle is not None:
        if bundle.name != "Vista.app" or bundle.is_symlink() or not bundle.is_dir() or binary != bundle / "Contents/MacOS/vista":
            raise ValueError("signed desktop bundle does not contain the selected executable")
        if list(binary.parent.iterdir()) != [binary]:
            raise ValueError("a desktop app must not package another executable")
        for path in sorted(bundle.rglob("*")):
            if path.is_symlink() or not (path.is_file() or path.is_dir()):
                raise ValueError("unexpected signed bundle entry")
            if path.is_file():
                entries[path.relative_to(bundle.parent).as_posix()] = path.read_bytes()
    elif system == "darwin":
        short_version = version.removeprefix("v") if re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", version) else "0.0.0"
        entries["Vista.app/Contents/Info.plist"] = plistlib.dumps({
            "CFBundleIdentifier": "io.github.willvar.VistaDesktop",
            "CFBundleExecutable": "vista",
            "CFBundleName": "Vista",
            "CFBundlePackageType": "APPL",
            "CFBundleShortVersionString": short_version,
            "CFBundleVersion": short_version,
            "NSHighResolutionCapable": True,
            "NSAppTransportSecurity": {"NSAllowsLocalNetworking": True},
        })
    return entries


def executable_entry(name):
    return Path(name).name in ("vista", "vista.exe")


def package(binary: Path, version: str, output: Path, bundle: Path | None = None) -> Path:
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.+_-]*", version):
        raise ValueError("invalid package version")
    info = metadata(binary)
    if not re.search(r"^\s*path\s+vista/desktop\s*$", info, re.M):
        raise ValueError("binary is not the unified Vista application")
    target = settings(info)
    if target.get("CGO_ENABLED") != "0":
        raise ValueError("Vista distributions must be built without CGO")
    target_os, arch = target["GOOS"], target["GOARCH"]
    if target_os not in ("windows", "linux", "darwin") or arch not in ("amd64", "arm64"):
        raise ValueError("unsupported distribution target")
    if bundle is not None and target_os != "darwin":
        raise ValueError("only the macOS desktop can include a signed app bundle")
    entries = archive_entries(binary, version, target_os, bundle)
    output.mkdir(parents=True, exist_ok=True)
    archive = output / archive_name(version, target_os, arch)
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
    archives = sorted(path for path in output.glob("vista_*")
                      if re.fullmatch(r"vista_[A-Za-z0-9.+_-]+_(linux|darwin|windows)_(amd64|arm64)\.(zip|tar\.gz)", path.name))
    (output / "SHA256SUMS").write_text(
        "".join(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n" for path in archives),
        encoding="utf-8",
    )
    return archive


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--bundle", type=Path, help="pack a prepared signed macOS app instead of generating one")
    parser.add_argument("--version", required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    print(package(args.binary, args.version, args.output_dir, args.bundle))
