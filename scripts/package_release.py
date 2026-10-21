#!/usr/bin/env python3
"""Package one Vista binary with its matching project and third-party notices."""

import argparse
import gzip
import hashlib
import io
import os
from pathlib import Path
import re
import subprocess
import tarfile
import zipfile


ROOT = Path(__file__).resolve().parent.parent


def package(binary: Path, version: str, output: Path) -> Path:
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.+_-]*", version):
        raise ValueError("invalid package version")
    data = binary.read_bytes()
    notices = (ROOT / "licenses/NOTICE").read_bytes()
    if notices not in data:
        raise ValueError("binary does not contain the current license notices; rebuild it first")
    metadata = subprocess.check_output(
        ["go", "version", "-m", str(binary.resolve())],
        env=dict(os.environ, GOWORK="off"), text=True,
    )
    settings = dict(re.findall(r"^\s*build\s+(GOOS|GOARCH)=(\S+)\s*$", metadata, re.M))
    target_os, arch = settings["GOOS"], settings["GOARCH"]
    binary_name = "vista.exe" if target_os == "windows" else "vista"
    entries = {
        binary_name: data,
        "LICENSE": (ROOT / "LICENSE").read_bytes(),
        "THIRD_PARTY_NOTICES": notices,
    }
    output.mkdir(parents=True, exist_ok=True)
    archive = output / f"vista_{version}_{target_os}_{arch}{'.zip' if target_os == 'windows' else '.tar.gz'}"
    if target_os == "windows":
        with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as package:
            for name, content in entries.items():
                info = zipfile.ZipInfo(name, (1980, 1, 1, 0, 0, 0))
                info.create_system = 3
                info.external_attr = (0o100755 if name == binary_name else 0o100644) << 16
                info.compress_type = zipfile.ZIP_DEFLATED
                package.writestr(info, content)
    else:
        with archive.open("wb") as raw:
            with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0, compresslevel=9) as compressed:
                with tarfile.open(fileobj=compressed, mode="w", format=tarfile.GNU_FORMAT) as package:
                    for name, content in entries.items():
                        info = tarfile.TarInfo(name)
                        info.size = len(content)
                        info.mode = 0o755 if name == binary_name else 0o644
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
    parser.add_argument("--version", required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    print(package(args.binary, args.version, args.output_dir))
