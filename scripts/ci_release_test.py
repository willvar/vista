import argparse
import hashlib
import json
from pathlib import Path
import re
import tempfile
import unittest
from unittest.mock import patch

from ci_native import ROOT, build_version
from ci_release import TARGETS, check_tag, collect


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        (ROOT / ".vista").mkdir(exist_ok=True)
        self.temporary = tempfile.TemporaryDirectory(dir=ROOT / ".vista")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.version, self.source = "v0.1.13", "a" * 40
        self.reports = []
        for target in sorted(TARGETS):
            system, arch = target.split("/")
            folder = self.root / "artifacts" / target.replace("/", "_")
            (folder / "dist").mkdir(parents=True)
            name = f"vista_{self.version}_{system}_{arch}" + (".zip" if system == "windows" else ".tar.gz")
            data = ("tested archive: " + target).encode()
            (folder / "dist" / name).write_bytes(data)
            report = {"target": target, "source_commit": self.source, "source_modified": False,
                      "version": self.version, "status": "passed", "tests": {"required_passed": ["native application"]},
                      "archive": name, "archive_sha256": hashlib.sha256(data).hexdigest(), "archive_size": len(data)}
            path = folder / "report.json"
            path.write_text(json.dumps(report))
            self.reports.append(path)

    def collect(self):
        return collect(self.version, self.source, self.root / "artifacts", self.root / "release")

    def test_only_stable_versions_or_dev_can_be_built(self):
        for version in ("dev", "v0.1.13", "v2.0.0"):
            self.assertEqual(build_version(version), version)
        for version in ("v01.2.3", "1.2.3", "v1.2.3-rc.1", "v1.2.3 extra", "../v1.2.3"):
            with self.subTest(version=version), self.assertRaises(argparse.ArgumentTypeError):
                build_version(version)

    def test_tag_must_be_annotated_and_match_tested_source(self):
        with patch("ci_release.subprocess.check_output", side_effect=["tag\n", self.source + "\n", "Release notes\n"]):
            check_tag(self.version, self.source)
        for replies in (["commit\n"], ["tag\n", "b" * 40], ["tag\n", self.source, ""]):
            with self.subTest(replies=replies), patch("ci_release.subprocess.check_output", side_effect=replies):
                with self.assertRaises(ValueError):
                    check_tag(self.version, self.source)
        with self.assertRaises(ValueError):
            check_tag("dev", self.source)

    def test_collects_exact_tested_bytes_and_sorted_checksums(self):
        text = self.collect()
        lines = text.splitlines()
        self.assertEqual(len(lines), len(TARGETS))
        names = []
        for line in lines:
            checksum, name = line.split("  ")
            self.assertEqual(hashlib.sha256((self.root / "release" / name).read_bytes()).hexdigest(), checksum)
            names.append(name)
        self.assertEqual(names, sorted(names))
        self.assertEqual((self.root / "release" / "SHA256SUMS").read_text(), text)

    def test_workflow_covers_all_six_release_targets(self):
        expected = {f"{system}/{arch}" for system in ("linux", "windows", "darwin") for arch in ("amd64", "arm64")}
        self.assertEqual(set(TARGETS), expected)
        workflow = (ROOT / ".github/workflows/validate.yml").read_text()
        targets = re.findall(r"^\s+target: (\S+)$", workflow, re.M)
        self.assertCountEqual(targets, TARGETS)

    def test_rejects_incomplete_platform_results(self):
        for path in self.reports:
            with self.subTest(target=path.parent.name):
                data = path.read_bytes()
                path.unlink()
                with self.assertRaises(ValueError):
                    self.collect()
                path.write_bytes(data)

    def test_rejects_wrong_source_version_or_unsuccessful_results(self):
        path = self.reports[0]
        original = json.loads(path.read_text())
        for key, value in (("source_commit", "b" * 40), ("source_modified", True),
                           ("version", "dev"), ("status", "failed"), ("tests", {}),
                           ("archive", "other.zip"), ("archive_size", 0)):
            with self.subTest(key=key), self.assertRaises(ValueError):
                path.write_text(json.dumps(dict(original, **{key: value})))
                self.collect()
        self.assertFalse((self.root / "release").exists())

    def test_rejects_duplicate_platform_results(self):
        self.reports[-1].write_bytes(self.reports[0].read_bytes())
        with self.assertRaises(ValueError):
            self.collect()

    def test_rejects_archive_changed_after_native_validation(self):
        path = self.reports[0]
        report = json.loads(path.read_text())
        (path.parent / "dist" / report["archive"]).write_bytes(b"changed archive")
        with self.assertRaises(ValueError):
            self.collect()


if __name__ == "__main__":
    unittest.main()
