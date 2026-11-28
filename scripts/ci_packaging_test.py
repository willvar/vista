from pathlib import Path
import plistlib
from tempfile import TemporaryDirectory
import unittest
from unittest.mock import patch

from ci_native import unpack
from package_release import ROOT, executable_location, package


def binary_info(system, arch="amd64", cgo="0", main="vista/desktop"):
    return f"path {main}\nbuild GOOS={system}\nbuild GOARCH={arch}\nbuild CGO_ENABLED={cgo}\n"


class ProductArchives(unittest.TestCase):
    def setUp(self):
        (ROOT / ".vista").mkdir(exist_ok=True)

    def test_each_target_includes_one_vista_executable(self):
        for system in ("linux", "darwin", "windows"):
            with self.subTest(system=system), TemporaryDirectory(dir=ROOT / ".vista") as temporary:
                root = Path(temporary)
                binary = root / "image"
                binary.write_bytes(b"Vista" + (ROOT / "licenses/NOTICE").read_bytes())
                with patch("package_release.metadata", return_value=binary_info(system)):
                    archive = package(binary, "v1.2.3", root / "dist")
                self.assertTrue(archive.name.startswith(f"vista_v1.2.3_{system}_"))
                dest = root / "unpacked"
                program = unpack(archive, binary, dest, system, "v1.2.3")
                self.assertEqual(program, dest / executable_location(system))
                self.assertEqual(program.read_bytes(), binary.read_bytes())
                self.assertFalse(list(dest.rglob("vista-server*")))
                self.assertFalse(list(dest.rglob("vista-desktop*")))
                if system == "darwin":
                    self.assertEqual(list(program.parent.iterdir()), [program])
                    settings = plistlib.loads((program.parents[1] / "Info.plist").read_bytes())
                    self.assertEqual(settings["CFBundleExecutable"], "vista")
                    self.assertEqual(settings["CFBundleShortVersionString"], "1.2.3")
                self.assertEqual(len((root / "dist/SHA256SUMS").read_text().splitlines()), 1)

    def test_preserves_prepared_macos_signature_files(self):
        with TemporaryDirectory(dir=ROOT / ".vista") as temporary:
            root = Path(temporary)
            app = root / "Vista.app"
            program = app / "Contents/MacOS/vista"
            program.parent.mkdir(parents=True)
            program.write_bytes(b"Vista" + (ROOT / "licenses/NOTICE").read_bytes())
            signature = app / "Contents/_CodeSignature/CodeResources"
            signature.parent.mkdir()
            signature.write_bytes(b"fixture signature metadata")
            (app / "Contents/Info.plist").write_bytes(plistlib.dumps({"CFBundleExecutable": "vista"}))
            with patch("package_release.metadata", return_value=binary_info("darwin", "arm64")):
                archive = package(program, "v1.2.3", root / "dist", app)
                unpack(archive, program, root / "extracted", "darwin", "v1.2.3", app)
                self.assertEqual((root / "extracted" / signature.relative_to(root)).read_bytes(), signature.read_bytes())
                program.with_name("unexpected-helper").write_bytes(b"unexpected")
                with self.assertRaisesRegex(ValueError, "another executable"):
                    package(program, "v1.2.3", root / "dist", app)

    def test_rejects_cgo_or_non_unified_executable(self):
        with TemporaryDirectory(dir=ROOT / ".vista") as temporary:
            root = Path(temporary)
            for info, diagnostic in [(binary_info("linux", cgo="1"), "without CGO"),
                                     (binary_info("linux", main="vista/cmd/vista-server"), "unified Vista")]:
                with self.subTest(diagnostic=diagnostic), patch("package_release.metadata", return_value=info):
                    with self.assertRaisesRegex(ValueError, diagnostic):
                        package(root / "vista", "dev", root / "dist")
