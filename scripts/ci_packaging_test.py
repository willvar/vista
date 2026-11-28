from pathlib import Path
import plistlib
from tempfile import TemporaryDirectory
import unittest
from unittest.mock import patch

from ci_native import unpack
from package_release import ROOT, executable_location, package


class ProductArchives(unittest.TestCase):
    def setUp(self):
        (ROOT / ".vista").mkdir(exist_ok=True)

    def test_distinct_products_include_one_executable_each(self):
        for system in ("linux", "darwin", "windows"):
            with self.subTest(system=system), TemporaryDirectory(dir=ROOT / ".vista") as temporary:
                root = Path(temporary)
                binary = root / "image"
                binary.write_bytes(b"service" + (ROOT / "licenses/NOTICE").read_bytes())
                for role, main in (("server", "vista/cmd/vista-server"), ("desktop", "vista/desktop")):
                    with patch("package_release.metadata", return_value=f"path {main}\nbuild GOOS={system}\nbuild GOARCH=amd64\n"):
                        archive = package(binary, "v1.2.3", root / "dist", role)
                    self.assertTrue(archive.name.startswith(f"vista-{role}_v1.2.3_"))
                    dest = root / role
                    program = unpack(archive, binary, dest, system, role, "v1.2.3")
                    self.assertEqual(program.read_bytes(), binary.read_bytes())
                    other = "server" if role == "desktop" else "desktop"
                    self.assertFalse((dest / executable_location(other, system)).exists())
                    self.assertFalse((dest / "vista").exists())
                    self.assertFalse((dest / "vista.exe").exists())
                    if role == "desktop" and system == "darwin":
                        self.assertFalse(program.with_name("vista").exists())
                        settings = plistlib.loads((program.parents[1] / "Info.plist").read_bytes())
                        self.assertEqual(settings["CFBundleExecutable"], "vista-desktop")
                        self.assertEqual(settings["CFBundleShortVersionString"], "1.2.3")
                self.assertEqual(len((root / "dist/SHA256SUMS").read_text().splitlines()), 2)

    def test_preserves_prepared_macos_signature_files(self):
        with TemporaryDirectory(dir=ROOT / ".vista") as temporary:
            root = Path(temporary)
            app = root / "Vista.app"
            program = app / "Contents/MacOS/vista-desktop"
            program.parent.mkdir(parents=True)
            program.write_bytes(b"desktop" + (ROOT / "licenses/NOTICE").read_bytes())
            signature = app / "Contents/_CodeSignature/CodeResources"
            signature.parent.mkdir()
            signature.write_bytes(b"fixture signature metadata")
            (app / "Contents/Info.plist").write_bytes(plistlib.dumps({"CFBundleExecutable": "vista-desktop"}))
            info = "path vista/desktop\nbuild GOOS=darwin\nbuild GOARCH=arm64\n"
            with patch("package_release.metadata", return_value=info):
                archive = package(program, "v1.2.3", root / "dist", "desktop", app)
                unpack(archive, program, root / "extracted", "darwin", "desktop", "v1.2.3", app)
                self.assertEqual((root / "extracted" / signature.relative_to(root)).read_bytes(), signature.read_bytes())
                program.with_name("vista-server").write_bytes(b"unexpected")
                with self.assertRaisesRegex(ValueError, "another executable"):
                    package(program, "v1.2.3", root / "dist", "desktop", app)

    def test_rejects_packaging_binary_as_wrong_product(self):
        with TemporaryDirectory(dir=ROOT / ".vista") as temporary:
            root = Path(temporary)
            with patch("package_release.metadata", return_value="path vista/cmd/vista-server\nbuild GOOS=linux\nbuild GOARCH=amd64\n"):
                with self.assertRaisesRegex(ValueError, "distribution role"):
                    package(root / "vista-desktop", "dev", root / "dist", "desktop")
