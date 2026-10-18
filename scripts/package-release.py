#!/usr/bin/env python3
"""Build Git-independent Linux releases with their complete runtime inventory."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parent.parent
PLATFORMS = {"x64": "amd64", "arm64": "arm64"}
# ShippedRuntimePath probes lua/huyang/rpc.lua beside bin/. Copy the entire
# tree, including future non-Lua assets. User config and language tools are external.
EXTRAS = ("LICENSE", "README.md", "contrib/systemd/huyang.service")


def payload(root):
    paths = [root / name for name in EXTRAS]
    lua = root / "lua"
    if lua.is_symlink() or not lua.is_dir():
        raise ValueError("lua must be a real directory")
    paths.extend(sorted(lua.rglob("*")))
    files = {}
    for path in paths:
        if path.is_symlink():
            raise ValueError(f"runtime symlinks are not supported: {path}")
        if path.is_dir():
            continue
        if not path.is_file():
            raise ValueError(f"missing runtime file: {path}")
        files[path.relative_to(root).as_posix()] = path.read_bytes()
    if "lua/huyang/rpc.lua" not in files:
        raise ValueError("missing kernel entry point lua/huyang/rpc.lua")
    return files


def write_archive(path, files):
    with path.open("wb") as raw:
        with gzip.GzipFile(filename="", fileobj=raw, mode="wb", mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.PAX_FORMAT) as archive:
                for name, data in sorted(files.items()):
                    entry = tarfile.TarInfo(name)
                    entry.size = len(data)
                    entry.mode = 0o755 if name == "bin/huyang" else 0o644
                    entry.mtime = entry.uid = entry.gid = 0
                    entry.uname = entry.gname = ""
                    archive.addfile(entry, io.BytesIO(data))


def package(version, destination):
    if not re.fullmatch(r"huyang-(?:v[0-9]+\.[0-9]+\.[0-9]+|[a-f0-9]{40})", version):
        raise ValueError("expected huyang-vX.Y.Z or huyang-COMMIT")
    output = Path(destination).resolve()
    output.mkdir(parents=True, exist_ok=True)
    runtime = payload(ROOT)
    with tempfile.TemporaryDirectory(prefix="huyang-build-") as temporary:
        for label, arch in PLATFORMS.items():
            binary = Path(temporary) / label / "huyang"
            binary.parent.mkdir()
            environment = dict(os.environ, GOOS="linux", GOARCH=arch, CGO_ENABLED="0")
            subprocess.run(
                ["go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-buildid=",
                 "-o", str(binary), "./cmd/huyang"],
                cwd=ROOT, env=environment, check=True,
            )
            files = dict(runtime, **{"bin/huyang": binary.read_bytes()})
            name = f"huyang-linux-{label}.tar.gz"
            archive = output / name
            manifest = {
                "version": version, "architecture": label,
                "files": {name: hashlib.sha256(data).hexdigest()
                          for name, data in sorted(files.items())},
            }
            files["manifest.json"] = (
                json.dumps(manifest, sort_keys=True, indent=2) + "\n"
            ).encode("utf-8")
            write_archive(archive, files)
            print(f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  {name}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version")
    parser.add_argument("destination")
    args = parser.parse_args()
    package(args.version, args.destination)
