"""Deliver original GAIA image attachments through Vista's message API."""

import argparse
import json
from pathlib import Path

import vista_runner as base
from dataset import IMAGE_TYPES, MAX_IMAGE_BYTES


def image_inputs(directory):
    images = []
    if directory.is_dir():
        for path in sorted(directory.iterdir()):
            media = IMAGE_TYPES.get(path.suffix.lower())
            if path.is_file() and media:
                size = path.stat().st_size
                if size > MAX_IMAGE_BYTES:
                    raise ValueError("Original GAIA image exceeds Vista 50 MiB input limit: " + path.name)
                images.append({"mediaType": media, "name": path.name, "path": str(path), "bytes": size})
    return images


def execute(root, logs, effort=None, files=Path("/app/files")):
    delivery = []
    def load():
        images = image_inputs(files)
        delivery.extend({"name": image["name"], "media_type": image["mediaType"],
                         "bytes": image["bytes"]} for image in images)
        return images
    logs.mkdir(parents=True, exist_ok=True)
    try:
        base.execute(root, logs, effort, image_loader=load)
    finally:
        (logs / "gaia-image-input.json").write_text(json.dumps({"images": delivery}, indent=2) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--logs-dir", type=Path, default=Path("/logs/agent"))
    parser.add_argument("--effort")
    parser.add_argument("--stop", action="store_true")
    args = parser.parse_args()
    if args.stop:
        base.stop(base.ROOT)
    else:
        execute(base.ROOT, args.logs_dir, args.effort)
