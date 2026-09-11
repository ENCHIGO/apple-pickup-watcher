"""Validate both package formats without needing cross-platform Rust binaries."""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
import zipfile


ROOT = Path(__file__).resolve().parents[1]


class PackageCliTests(unittest.TestCase):
    def test_packages_use_utf8_and_include_binary_skill_and_matching_checksums(self):
        for target, extension, binary_name in [
            ("aarch64-apple-darwin", ".tar.gz", "apw"),
            ("x86_64-pc-windows-msvc", ".zip", "apw.exe"),
        ]:
            with self.subTest(target=target), tempfile.TemporaryDirectory() as temporary:
                work = Path(temporary)
                binary = work / "fixture.bin"
                payload = b"APW packaging fixture\x00\xff"
                binary.write_bytes(payload)
                # Unix: force ASCII. Windows: retain the system legacy code page.
                # Cargo.toml contains Chinese comments and must be read as UTF-8.
                environment = {**os.environ, "LC_ALL": "C", "PYTHONUTF8": "0", "PYTHONCOERCECLOCALE": "0"}
                result = subprocess.run(
                    [sys.executable, str(ROOT / "scripts/package-cli.py"),
                     "--binary", str(binary), "--target", target, "--output", str(work / "out")],
                    env=environment, capture_output=True, timeout=30,
                )
                self.assertEqual(result.returncode, 0, result.stderr.decode("utf-8", errors="replace"))
                archive = next((work / "out").glob("*" + extension))
                checksum = archive.with_name(archive.name + ".sha256").read_text(encoding="utf-8").split()[0]
                self.assertEqual(checksum, hashlib.sha256(archive.read_bytes()).hexdigest())
                if extension == ".zip":
                    with zipfile.ZipFile(archive) as bundle:
                        files = {name.split("/", 1)[1]: bundle.read(name) for name in bundle.namelist()}
                else:
                    with tarfile.open(archive) as bundle:
                        files = {member.name.split("/", 1)[1]: bundle.extractfile(member).read()
                                 for member in bundle.getmembers() if member.isfile()}
                self.assertEqual(files[binary_name], payload)
                manifest = json.loads(files["manifest.json"])
                self.assertEqual(manifest["target"], target)
                self.assertEqual(manifest["binary"], binary_name)
                self.assertEqual(manifest["sha256"], hashlib.sha256(payload).hexdigest())
                for filename in ["LICENSE", "NOTICE", "README.md",
                                 "skills/apple-pickup-watcher/SKILL.md",
                                 "skills/apple-pickup-watcher/agents/openai.yaml"]:
                    self.assertTrue(files[filename], filename)


if __name__ == "__main__":
    unittest.main()
