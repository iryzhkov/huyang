#!/usr/bin/env python3
"""Collect ONLY M9 reports, never the run tree. Invoke with Python -I -B."""
import sys
sys.dont_write_bytecode = True
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import runpy
import stat
import tarfile

LIMIT = 63 * 1024 * 1024  # Strictly below the 64 MiB transport limit.
TOP = {
    "versions.json", "binary.json", "preflight-commands.log",
    "tool-manifest.txt", "tool-download-sha256.txt", "tool-package-lock.json",
    "before-tool-hashes.json", "after-tool-hashes.json",
    "before-home-metadata.json", "after-home-metadata.json",
}
CASE = re.compile(
    r"(?:lsp\.jsonl|wire-[a-z]+\.jsonl|[0-9]{3,}-[a-z_]+\.json|exec\.[0-9]+|"
    r"coverage\.json|[a-z-]*hashes[a-z-]*\.json|fixture-commands\.log|service\.log|"
    r"plain-[a-z-]+\.txt|raw-publication-summary\.json|"
    r"historical-observation-plan\.json|historical-trace-cut\.jsonl|historical-observation\.json)"
)


class CappedWriter:
    def __init__(self, raw):
        self.raw = raw

    def write(self, body):
        if self.raw.tell() + len(body) >= LIMIT:
            raise RuntimeError("COMPLETE REQUIRED EVIDENCE EXCEEDS 63 MiB CAP")
        return self.raw.write(body)

    def flush(self):
        self.raw.flush()

    def tell(self):
        return self.raw.tell()


def main():
    guard = runpy.run_path(str(Path(__file__).with_name("safety.py")))
    home = guard["scratch_home"]()  # Before any output, including partial archives.
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("run")
    parser.add_argument("archive")
    parser.add_argument("--go-log", required=True)
    parser.add_argument("--test-exit", required=True, type=int)
    args = parser.parse_args()
    run = guard["owned_path"](home, args.run)
    archive = guard["owned_path"](home, args.archive)
    go_log = guard["owned_path"](home, args.go_log)
    if go_log != run.parent / "python-probe-go-test.log":
        raise SystemExit("--go-log must be the named log beside the private run")
    if not run.is_dir() or archive.exists() or archive.is_relative_to(run):
        raise SystemExit("require existing run and a new archive outside it")
    if archive.suffixes[-2:] != [".tar", ".gz"]:
        raise SystemExit("archive must end .tar.gz")
    # No recursive collection: these are the only traversed directories.
    candidates = [(run / name, name) for name in sorted(TOP) if os.path.lexists(run / name)]
    cases = []
    for directory in sorted(run.iterdir()):
        if re.fullmatch(r"[0-9]{2}", directory.name):
            if directory.is_symlink() or not directory.is_dir():
                raise SystemExit("case must be a real directory")
            cases.append(directory.name)
            for path in sorted(directory.iterdir()):
                if CASE.fullmatch(path.name):
                    candidates.append((path, directory.name + "/" + path.name))
    candidates.append((go_log, "go-test.log"))
    if len(candidates) > 20000:
        raise SystemExit("required evidence exceeds collector entry bound; nothing truncated")
    files, payload = [], 0
    for path, name in candidates:
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid():
            raise SystemExit("evidence must be owned regular files: " + str(path))
        payload += info.st_size
        if payload >= LIMIT:
            raise SystemExit("COMPLETE REQUIRED EVIDENCE EXCEEDS 63 MiB CAP; nothing truncated or archived")
        files.append((path, name, info.st_size))
    manifest = {
        "complete_collection": True, "test_exit": args.test_exit,
        "runtime_acceptance_claim": False, "cap_bytes": LIMIT,
        "case_directories": cases,
        "coverage_present": [name for name in cases if (run / name / "coverage.json").is_file()],
        "missing_top_evidence": sorted(TOP - {name for _, name, _ in files}),
        "files": [],
    }
    # The output path is explicit and beneath guarded HOME; no temp outside it.
    partial = archive.with_name(archive.name + ".partial")
    fd = os.open(partial, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "wb") as raw:
            with gzip.GzipFile(filename="", fileobj=CappedWriter(raw), mode="wb", mtime=0) as compressed:
                with tarfile.open(fileobj=compressed, mode="w|", format=tarfile.USTAR_FORMAT) as tar:
                    for path, name, size in files:
                        source_fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
                        with os.fdopen(source_fd, "rb") as source:
                            before = os.fstat(source.fileno())
                            if before.st_size != size or not stat.S_ISREG(before.st_mode):
                                raise RuntimeError("evidence changed before collection: " + name)
                            body = source.read(size + 1)
                            after = os.fstat(source.fileno())
                            if len(body) != size or (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns, before.st_ctime_ns) != (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns):
                                raise RuntimeError("evidence changed during collection: " + name)
                        manifest["files"].append({"path": name, "bytes": size, "sha256": hashlib.sha256(body).hexdigest()})
                        entry = tarfile.TarInfo(name)
                        entry.size, entry.mode, entry.mtime = size, 0o600, 0
                        tar.addfile(entry, io.BytesIO(body))
                        if raw.tell() >= LIMIT:
                            raise RuntimeError("COMPLETE REQUIRED EVIDENCE EXCEEDS 63 MiB CAP")
                    body = (json.dumps(manifest, indent=2) + "\n").encode()
                    entry = tarfile.TarInfo("collection.json")
                    entry.size, entry.mode, entry.mtime = len(body), 0o600, 0
                    tar.addfile(entry, io.BytesIO(body))
            if raw.tell() >= LIMIT:
                raise RuntimeError("COMPLETE REQUIRED EVIDENCE EXCEEDS 63 MiB CAP")
        os.link(partial, archive)  # Refuse concurrent destination replacement.
        partial.unlink()
    except BaseException:
        partial.unlink(missing_ok=True)
        raise
    print(str(archive) + ": complete allowlisted collection, " + str(archive.stat().st_size) + " bytes")


if __name__ == "__main__":
    main()
