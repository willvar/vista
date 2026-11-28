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


def desktop_location(system):
    return {"windows": "vista-desktop.exe", "linux": "vista-desktop",
            "darwin": "Vista.app/Contents/MacOS/vista-desktop"}[system]


PRODUCT_PATHS = {"server": "vista/cmd/vista-server", "desktop": "vista/desktop"}


def executable_location(role, system):
    if role == "desktop":
        return desktop_location(system)
    return "vista-server.exe" if system == "windows" else "vista-server"


def archive_name(role, version, system, arch):
    return f"vista-{role}_{version}_{system}_{arch}" + (".zip" if system == "windows" else ".tar.gz")


def archive_entries(binary, version, system, role, bundle=None):
    data = binary.read_bytes()
    notices = (ROOT / "licenses/NOTICE").read_bytes()
    if notices not in data:
        raise ValueError("binary does not contain the current license notices; rebuild it first")
    entries = {
        executable_location(role, system): data,
        "LICENSE": (ROOT / "LICENSE").read_bytes(),
        "THIRD_PARTY_NOTICES": notices,
    }
    if role == "desktop" and system == "linux":
        entries["share/applications/io.github.willvar.VistaDesktop.desktop"] = (
            ROOT / "desktop/vista.desktop").read_bytes()
        entries["share/icons/hicolor/64x64/apps/io.github.willvar.VistaDesktop.png"] = (
            ROOT / "desktop/vista.png").read_bytes()
    elif role == "desktop" and system == "darwin" and bundle is not None:
        if bundle.name != "Vista.app" or bundle.is_symlink() or not bundle.is_dir() or binary != bundle / "Contents/MacOS/vista-desktop":
            raise ValueError("signed desktop bundle does not contain the selected executable")
        if list(binary.parent.iterdir()) != [binary]:
            raise ValueError("a desktop app must not package another executable")
        for path in sorted(bundle.rglob("*")):
            if path.is_symlink() or not (path.is_file() or path.is_dir()):
                raise ValueError("unexpected signed bundle entry")
            if path.is_file():
                entries[path.relative_to(bundle.parent).as_posix()] = path.read_bytes()
    elif role == "desktop" and system == "darwin":
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
    return Path(name).name in ("vista-server", "vista-server.exe", "vista-desktop", "vista-desktop.exe")


def package(binary: Path, version: str, output: Path, role: str, bundle: Path | None = None) -> Path:
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.+_-]*", version):
        raise ValueError("invalid package version")
    info = metadata(binary)
    if role not in PRODUCT_PATHS or not re.search(r"^\s*path\s+" + re.escape(PRODUCT_PATHS[role]) + r"\s*$", info, re.M):
        raise ValueError("binary does not match the requested distribution role")
    target = settings(info)
    target_os, arch = target["GOOS"], target["GOARCH"]
    if target_os not in ("windows", "linux", "darwin") or arch not in ("amd64", "arm64"):
        raise ValueError("unsupported distribution target")
    if bundle is not None and (role != "desktop" or target_os != "darwin"):
        raise ValueError("only the macOS desktop can include a signed app bundle")
    entries = archive_entries(binary, version, target_os, role, bundle)
    output.mkdir(parents=True, exist_ok=True)
    archive = output / archive_name(role, version, target_os, arch)
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
    archives = sorted(path for product in PRODUCT_PATHS
                      for pattern in (f"vista-{product}_*.zip", f"vista-{product}_*.tar.gz")
                      for path in output.glob(pattern))
    (output / "SHA256SUMS").write_text(
        "".join(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n" for path in archives),
        encoding="utf-8",
    )
    return archive


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--role", choices=PRODUCT_PATHS, default="server")
    parser.add_argument("--bundle", type=Path, help="pack a prepared signed macOS app instead of generating one")
    parser.add_argument("--version", required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    print(package(args.binary, args.version, args.output_dir, args.role, args.bundle))
