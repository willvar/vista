import plistlib
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest
from unittest.mock import patch

from ci_native import unpack
from package_release import ROOT, desktop_location, package


class DesktopArchives(unittest.TestCase):
    def setUp(self):
        (ROOT / ".vista").mkdir(exist_ok=True)

    def test_desktop_archive_layout(self):
        for system in ("linux", "darwin", "windows"):
            with self.subTest(system=system), TemporaryDirectory(dir=ROOT / ".vista") as temporary:
                root = Path(temporary)
                binary = root / ("vista.exe" if system == "windows" else "vista")
                binary.write_bytes(b"service" + (ROOT / "licenses/NOTICE").read_bytes())
                desktop = root / "shell"
                desktop.write_bytes(b"desktop")
                common = (f"build GOOS={system}\nbuild GOARCH=amd64\n"
                          "build vcs.revision=source\nbuild vcs.modified=false\n")
                with patch("package_release.metadata", side_effect=[common, "path vista/desktop\n" + common]):
                    archive = package(binary, "v1.2.3", root / "dist", desktop)
                service = unpack(archive, binary, root / "unpacked", desktop, system, "v1.2.3")
                self.assertEqual(service.read_bytes(), binary.read_bytes())
                self.assertEqual((service.parent / desktop_location(system)).read_bytes(), b"desktop")
                if system == "darwin":
                    bundle = service.parent / "Vista.app/Contents"
                    self.assertEqual((bundle / "MacOS/vista").read_bytes(), binary.read_bytes())
                    settings = plistlib.loads((bundle / "Info.plist").read_bytes())
                    self.assertEqual(settings["CFBundleExecutable"], "vista-desktop")
                    self.assertEqual(settings["CFBundleShortVersionString"], "1.2.3")
                elif system == "linux":
                    icon = service.parent / "share/icons/hicolor/64x64/apps/io.github.willvar.VistaDesktop.png"
                    self.assertTrue(icon.is_file())

    def test_rejects_mismatched_desktop_source(self):
        with TemporaryDirectory(dir=ROOT / ".vista") as temporary:
            root = Path(temporary)
            binary = root / "vista"
            binary.write_bytes((ROOT / "licenses/NOTICE").read_bytes())
            common = "build GOOS=linux\nbuild GOARCH=amd64\nbuild vcs.modified=false\n"
            with patch("package_release.metadata", side_effect=[common + "build vcs.revision=service\n",
                                                               "path vista/desktop\n" + common + "build vcs.revision=other\n"]):
                with self.assertRaisesRegex(ValueError, "match the service"):
                    package(binary, "dev", root / "dist", root / "shell")
