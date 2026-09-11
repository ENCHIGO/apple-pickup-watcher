#!/usr/bin/env python3
"""Package a built APW CLI with its agent skill, documentation and licenses."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import tarfile
import tempfile
import shutil
import tomllib
import zipfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--target", required=True, help="Rust target triple of the supplied binary")
    parser.add_argument("--output", type=Path, default=Path("target/cli-dist"))
    args = parser.parse_args()
    if not re.fullmatch(r"[a-zA-Z0-9_-]+", args.target):
        parser.error("invalid target triple")
    if not args.binary.is_file():
        parser.error(f"binary not found: {args.binary}")
    root = Path(__file__).resolve().parents[1]
    version = tomllib.loads((root / "Cargo.toml").read_text())["workspace"]["package"]["version"]
    name = f"apw-cli-v{version}-{args.target}"
    args.output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="apw-cli-package-") as temporary:
        package = Path(temporary) / name
        package.mkdir()
        binary_name = "apw.exe" if "windows" in args.target else "apw"
        shutil.copy2(args.binary, package / binary_name)
        (package / binary_name).chmod(0o755)
        for filename in ("LICENSE", "NOTICE"):
            shutil.copy2(root / filename, package / filename)
        shutil.copy2(root / "docs/cli.md", package / "README.md")
        shutil.copytree(root / "skills/apple-pickup-watcher", package / "skills/apple-pickup-watcher")
        manifest = {"version": version, "target": args.target, "binary": binary_name,
                    "sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest(), "schemaVersion": 1}
        (package / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        if "windows" in args.target:
            archive = args.output / f"{name}.zip"
            with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as bundle:
                for path in sorted(package.rglob("*")):
                    if path.is_file():
                        bundle.write(path, path.relative_to(package.parent))
        else:
            archive = args.output / f"{name}.tar.gz"
            with tarfile.open(archive, "w:gz") as bundle:
                bundle.add(package, arcname=name)
    checksum = hashlib.sha256(archive.read_bytes()).hexdigest()
    archive.with_name(archive.name + ".sha256").write_text(f"{checksum}  {archive.name}\n")
    print(archive.resolve())


if __name__ == "__main__":
    main()
