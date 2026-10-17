#!/usr/bin/env python3
"""Read-only preflight shared by setup and the evidence collector."""
import sys
sys.dont_write_bytecode = True
if sys.version_info < (3, 9):
    raise SystemExit("bootstrap Python >=3.9 required")
import os
from pathlib import Path


def scratch_home():
    supplied = os.environ.get("HOME", "")
    expected = (os.environ.get("HUYANG_M9_EXPECTED_HOME") or "/home/job")
    home = Path(supplied)
    if not home.is_absolute() or supplied != expected or str(home.resolve(strict=True)) != supplied:
        raise SystemExit("HOME must equal canonical expected scratch HOME")
    if (supplied in ("/", "/root") or supplied.startswith(("/root/", "/tmp/"))
            or (supplied.startswith("/home/") and supplied != "/home/job"
                and not supplied.startswith("/home/job/"))):
        raise SystemExit("refusing real or temporary HOME; use isolated job scratch")
    mapping = Path("/proc/self/uid_map").read_text().split()
    if len(mapping) < 3 or mapping == ["0", "0", "4294967295"]:
        raise SystemExit("non-initial UID namespace required before any writes")
    info = home.stat()
    if not home.is_dir() or info.st_uid != os.geteuid() or info.st_mode & 0o022:
        raise SystemExit("scratch HOME must be owned and not writable by others")
    return home


def owned_path(home, value):
    path = Path(value)
    if not path.is_absolute():
        raise SystemExit("write roots must be absolute")
    resolved = path.resolve(strict=False)
    if not resolved.is_relative_to(home) or resolved == home:
        raise SystemExit("every write root must resolve strictly beneath scratch HOME")
    return resolved


if __name__ == "__main__":
    import sys
    home = scratch_home()
    values = sys.argv[1:]
    fresh = bool(values and values[0] == "--fresh-prefix")
    if fresh:
        values = values[1:]
        if len(values) != 1:
            raise SystemExit("--fresh-prefix requires exactly one prefix")
    for value in values:
        path = owned_path(home, value)
        if fresh and path.exists() and (not path.is_dir() or any(path.iterdir())):
            raise SystemExit("provisioning requires a new or empty prefix; never reuse tools")
