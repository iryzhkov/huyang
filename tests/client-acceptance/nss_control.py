"""Opt-in, job-private NSS acquisition control for the pinned native Neovim.

No account synthesis, HOME rewrite, audit exception or producer substitution.
"""
import json
import os
from pathlib import Path
import re
import stat
import sys

from common import digest, require, write_json

PACKAGE_URL = "https://deb.debian.org/debian/pool/main/n/nss-wrapper/libnss-wrapper_1.1.16-1_amd64.deb"
PACKAGE_SHA256 = "7f1c57d82bf64e31cc5bbd2e254a460c1236ce19f847fd0079839f28ae390ad9"
LIBRARY_SHA256 = "f78a1f924b674de69b65975f3bc61bbc9ebe0918125b5a322217f031fc928413"
NATIVE_SHA256 = "23489b431f020180b1cd82268a0ffefec8165aa4e61224188f8a6e1d68a8681c"
LIBRARY_RELATIVE = "private/usr/lib/x86_64-linux-gnu/libnss_wrapper.so"
ACCOUNTS = {"passwd": b"job:x:0:0::/home/job:/bin/sh\n", "group": b"job:x:0:\n"}
LOADER_KEYS = {"LD_PRELOAD", "LD_AUDIT", "LD_LIBRARY_PATH", "LD_DEBUG", "LD_DEBUG_OUTPUT"}


def override_keys(env):
    return sorted(k for k in env if k in LOADER_KEYS or k.startswith("NSS_WRAPPER_"))


def require_clean(env):
    require(not override_keys(env), "ambient loader/NSS override refused")


def bounded(path, maximum):
    path = Path(path)
    require(path.is_absolute() and path.resolve() == path, "noncanonical NSS input: " + str(path))
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and info.st_size <= maximum, "unbounded NSS input: " + str(path))
        raw = stream.read(maximum + 1)
    require(len(raw) <= maximum, "NSS input grew beyond bound")
    return raw


def identity():
    status = Path("/proc/self/status").read_text()
    return {"status": [line.split() for line in status.splitlines() if line.startswith(("Uid:", "Gid:", "Groups:"))],
            "uid": list(os.getresuid()), "gid": list(os.getresgid()), "groups": os.getgroups(),
            "uid_map": Path("/proc/self/uid_map").read_text(),
            "gid_map": Path("/proc/self/gid_map").read_text(),
            "namespaces": {n: os.readlink("/proc/self/ns/" + n) for n in ("user", "mnt", "pid", "net")},
            "HOME": os.environ.get("HOME")}


def validate_mounts(mountinfo, ident):
    """Pure guard for agent-a's exact per-job synthetic account mounts."""
    require(ident["HOME"] == "/home/job" and ident["uid"] == ident["gid"] == [0, 0, 0]
            and ident["groups"] == [0], "unexpected disposable job identity/HOME")
    require(ident["status"] == [["Uid:", "0", "0", "0", "0"], ["Gid:", "0", "0", "0", "0"], ["Groups:", "0"]],
            "unexpected kernel UID/GID/FSUID/FSGID/group status")
    require(ident["uid_map"].split() == ident["gid_map"].split() == ["0", "0", "1"],
            "unexpected disposable UID/GID maps")
    require(ident["namespaces"]["user"] != "user:[4026531837]", "initial user namespace refused")
    mounts = [line.split() for line in mountinfo.splitlines()]
    require(mounts and all(len(p) >= 10 and "-" in p[6:] for p in mounts), "malformed mountinfo")
    require(not any(p[4] == "/etc" for p in mounts), "unexpected whole /etc mount")

    def exact(path):
        rows = [p for p in mounts if p[4] == path]
        require(len(rows) == 1, "missing/duplicate required mount: " + path)
        return rows[0]

    root, home = exact("/"), exact("/home/job")
    require(root[3] == "/newroot" and "ro" in root[5].split(",")
            and root[root.index("-") + 1] == "tmpfs", "unexpected disposable root")
    # agent-a has three zero-indexed slots. Never accept host or sibling-job accounts.
    match = re.fullmatch(r"(/slot-[0-2]/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/\.chaldea-home", home[3])
    require(match is not None and "rw" in home[5].split(","), "unexpected disposable HOME mount")
    require(home[home.index("-") + 1] not in ("tmpfs", "ramfs"), "HOME must use disk scratch")
    assert match is not None
    prefix = match.group(1)
    for name in ACCOUNTS:
        entry = exact("/etc/" + name)
        require(entry[3] == prefix + ".identity/" + name and entry[2] == home[2]
                and "ro" in entry[5].split(","), "not this job's read-only synthetic account mount")
    policy = exact("/etc/nsswitch.conf")
    require(policy[3] == "/etc/nsswitch.conf" and "ro" in policy[5].split(","),
            "unexpected public NSS policy mount")


def validate_records(contents, policy):
    require(contents == ACCOUNTS, "unexpected full synthetic passwd/group records")
    lines = [line.split("#", 1)[0].strip() for line in policy.decode("utf-8").splitlines()]
    for name in ACCOUNTS:
        values = [line.split(":", 1)[1].split() for line in lines if re.match(name + r"\s*:", line)]
        require(values == [["files"]], "files-only NSS policy required for " + name)


def sources(job):
    """Read and validate mount provenance before opening account data or writing."""
    require_clean(os.environ)
    job, home = Path(job), Path("/home/job")
    require(home.resolve() == home and home.is_dir() and home.stat().st_uid == 0, "unsafe scratch HOME")
    require(job.is_absolute() and job.resolve() == job and job != home and job.is_relative_to(home),
            "NSS job must be canonical below scratch HOME")
    for ancestor in (job, *job.parents):
        if ancestor.is_relative_to(home) and ancestor.exists():
            info = ancestor.stat()
            require(ancestor.is_dir() and not ancestor.is_symlink() and info.st_uid == 0
                    and not info.st_mode & 0o022, "unsafe NSS scratch ancestor")
    ident = identity()
    mountinfo = Path("/proc/self/mountinfo").read_text()
    validate_mounts(mountinfo, ident)
    contents = {}
    for name in ACCOUNTS:
        path = Path("/etc") / name
        info = path.lstat()
        require(stat.S_ISREG(info.st_mode) and info.st_uid == info.st_gid == 0
                and stat.S_IMODE(info.st_mode) == 0o444, "unexpected synthetic account metadata")
        contents[name] = bounded(path, 1024)
    policy = bounded("/etc/nsswitch.conf", 65536)
    validate_records(contents, policy)
    return {"identity": ident, "mountinfo": mountinfo, "nsswitch_sha256": digest(policy),
            "nsswitch": policy.decode("utf-8")}, contents


def paths(job):
    root = Path(job) / "nss"
    return root, root / "nss.deb", root / LIBRARY_RELATIVE


def provision(job, env, command):
    """Called only by explicit bootstrap, using its existing bounded command helper."""
    require_clean(env)
    source, contents = sources(job)
    root, package, library = paths(job)
    require(not root.exists() and not root.is_symlink(), "NSS provision root must be new")
    root.mkdir(mode=0o700)
    command(["curl", "--fail", "--location", "--silent", "--show-error", "--proto", "=https",
             "--tlsv1.2", "--connect-timeout", "15", "--max-time", "120",
             "--max-filesize", str(1 << 20), "-o", package, PACKAGE_URL],
            env, job, root / "download.log", 125)
    require(digest(bounded(package, 1 << 20)) == PACKAGE_SHA256, "NSS package pin mismatch")
    private = root / "private"
    private.mkdir(mode=0o700)
    command(["dpkg-deb", "-x", package, private], env, job, root / "extract.log", 30)
    require(digest(bounded(library, 1 << 20)) == LIBRARY_SHA256, "NSS library pin mismatch")
    again, fresh = sources(job)
    require(source == again and contents == fresh, "NSS account sources changed during provision")
    for name, raw in contents.items():
        path = root / name
        with path.open("xb") as stream:
            stream.write(raw)
        path.chmod(0o400)
        require(bounded(path, 1024) == raw, "NSS fixture bytes differ from source")
    report = {"status": "provisioned-not-accepted", "package_url": PACKAGE_URL,
              "package_sha256": PACKAGE_SHA256, "library_sha256": LIBRARY_SHA256,
              "package_version": "1.1.16-1", "native_sha256": NATIVE_SHA256,
              "fixture_sha256": {n: digest(raw) for n, raw in contents.items()}, "source": source}
    write_json(root / "manifest.json", report)
    return report


def verify(job, native=None):
    source, contents = sources(job)
    root, package, library = paths(job)
    report = json.loads(bounded(root / "manifest.json", 65536))
    require(report["status"] == "provisioned-not-accepted" and report["package_url"] == PACKAGE_URL
            and report["package_version"] == "1.1.16-1" and report["native_sha256"] == NATIVE_SHA256,
            "NSS provision manifest changed")
    require(report["source"] == source, "NSS source identity/mounts/policy changed")
    for path, pin, key in ((package, PACKAGE_SHA256, "package_sha256"),
                           (library, LIBRARY_SHA256, "library_sha256")):
        require(report[key] == pin and digest(bounded(path, 1 << 20)) == pin, "NSS sealed pin mismatch")
    for name, raw in contents.items():
        require(bounded(root / name, 1024) == raw and report["fixture_sha256"][name] == digest(raw),
                "NSS fixture/source/hash mismatch")
    if native is not None:
        native = Path(native)
        require(native.is_relative_to(Path("/home/job")) and os.access(native, os.X_OK)
                and digest(bounded(native, 64 << 20)) == NATIVE_SHA256, "native Nvim pin/path mismatch")
    return report


# nss_wrapper 1.1.16 nwrap_init caches getenv pointers once; its constructor
# deliberately does not initialize them. glibc 2.41 unsetenv retains the strings.
# Initialize before permanent cleanup, then require the same full lookup after it.
# This prefix runs before LSP enable and every workspace warm-probe child.
NATIVE_INIT_LUA = """local ok, err = pcall(function()
local ffi = require('ffi')
ffi.cdef[[
  bool nss_wrapper_enabled(void);
  struct passwd {
    char *pw_name; char *pw_passwd; unsigned int pw_uid; unsigned int pw_gid;
    char *pw_gecos; char *pw_dir; char *pw_shell;
  };
  struct passwd *getpwuid(unsigned int uid);
]]
assert(ffi.C.nss_wrapper_enabled(), 'NSS wrapper not initialized')
for key, _ in pairs(vim.fn.environ()) do
  if key:match('^NSS_WRAPPER_') or key == 'LD_PRELOAD' or key == 'LD_AUDIT'
    or key == 'LD_LIBRARY_PATH' or key == 'LD_DEBUG' or key == 'LD_DEBUG_OUTPUT' then
    vim.env[key] = nil
  end
end
local remaining = {}
for key, _ in pairs(vim.fn.environ()) do
  if key:match('^NSS_WRAPPER_') or key == 'LD_PRELOAD' or key == 'LD_AUDIT'
    or key == 'LD_LIBRARY_PATH' or key == 'LD_DEBUG' or key == 'LD_DEBUG_OUTPUT' then
    remaining[#remaining + 1] = key
  end
end
assert(#remaining == 0, 'NSS/loader environment survived native init')
assert(ffi.C.nss_wrapper_enabled(), 'cached NSS wrapper disabled after cleanup')
local pw = ffi.C.getpwuid(0)
assert(pw ~= nil, 'native getpwuid failed after environment cleanup')
local passwd = {ffi.string(pw.pw_name), ffi.string(pw.pw_passwd),
  tonumber(pw.pw_uid), tonumber(pw.pw_gid), ffi.string(pw.pw_gecos),
  ffi.string(pw.pw_dir), ffi.string(pw.pw_shell)}
assert(vim.deep_equal(passwd, {'job', 'x', 0, 0, '', '/home/job', '/bin/sh'}),
  'native passwd record changed after environment cleanup')
assert(vim.env.HOME == '/home/job', 'native HOME changed')
local proof = vim.fn.fnamemodify(vim.env.HUYANG_HEADLESS_INIT, ':h')
  .. '/nss-native-' .. vim.uv.os_getpid() .. '.json'
local output = assert(io.open(proof, 'w'))
assert(output:write(vim.json.encode({pid = vim.uv.os_getpid(), HOME = vim.env.HOME,
  cached_wrapper_enabled = true, loader_nss_keys = remaining, passwd = passwd})))
assert(output:close())
end)
if not ok then
  io.stderr:write('native NSS init failed: ' .. tostring(err))
  os.exit(1)
end
"""


def launcher_source(native, job):
    return ("#!" + sys.executable + "\nimport sys\nsys.dont_write_bytecode = True\n"
            + "sys.path.insert(0, " + repr(str(Path(__file__).resolve().parent)) + ")\n"
            + "from nss_control import exec_native\n"
            + "exec_native(" + repr(str(native)) + ", " + repr(str(job)) + ", sys.argv[1:])\n")


def exec_native(native, job, argv):
    # Interpreter, service, tracer and model clients reach here without NSS.
    # Only execve's explicit native environment receives the control.
    verify(job, native)
    init = Path(os.environ.get("HUYANG_HEADLESS_INIT", ""))
    require(init.is_absolute() and init.resolve() == init and init.parent.parent == Path(job) / "c"
            and init.parent.name in ("claude", "codex", "opencode") and init.name == "init.lua",
            "NSS native exec requires the owned case init")
    require("--embed" in argv and "--headless" in argv and "--clean" in argv
            and argv.count("-u") == 1 and argv.index("-u") + 1 < len(argv)
            and argv[argv.index("-u") + 1] == str(init), "native exec bypasses owned NSS cleanup init")
    seal = json.loads(bounded(init.parent / "nss-init.json", 4096))
    raw = bounded(init, 65536)
    require(raw.startswith(NATIVE_INIT_LUA.encode()) and digest(raw) == seal["sha256"],
            "native NSS cleanup init changed")
    root, _, library = paths(job)
    env = dict(os.environ, LD_PRELOAD=str(library), NSS_WRAPPER_PASSWD=str(root / "passwd"),
               NSS_WRAPPER_GROUP=str(root / "group"), LD_DEBUG="bindings",
               LD_DEBUG_OUTPUT=str(init.parent / "nss-bindings"))
    os.execve(str(native), [str(native), *argv], env)


def native_binding_lines(raw, native, library):
    return [line for line in raw.decode("utf-8").splitlines()
            if "binding file " + str(native) + " [0] to " + str(library) + " [0]:" in line
            and line.endswith("normal symbol " + chr(96) + "getpwuid' [GLIBC_2.2.5]")]


def binding_program(raw, native, pid):
    """Loader startup markers identify the executable, independent of symbol lines."""
    markers = []
    for line in raw.decode("utf-8").splitlines():
        match = re.fullmatch(r"\s*(\d+):\s+(initialize program|transferring control): (.+)", line)
        if match:
            markers.append({"pid": int(match[1]), "kind": match[2], "program": match[3]})
    foreign = sorted({m["program"] for m in markers if m["program"] != str(native)})
    require(not foreign, "NSS/loader environment leak to non-native process: " + ", ".join(foreign))
    require(markers == [{"pid": pid, "kind": kind, "program": str(native)}
                        for kind in ("initialize program", "transferring control")],
            "missing/mixed native loader program identity; possible NSS/loader environment leak")
    return markers


def finish_case(case, job, native):
    manifest = verify(job, native)
    _, _, library = paths(job)
    files = sorted(Path(case).glob("nss-bindings.*"))
    require(0 < len(files) <= 512, "missing/overlarge native NSS binding evidence")
    total, evidence = 0, []
    for path in files:
        require(re.fullmatch(r"nss-bindings\.\d+", path.name), "unexpected NSS binding artifact")
        raw = bounded(path, 8 << 20)
        total += len(raw)
        require(total <= 55 << 20, "NSS evidence exceeds bound; preserve all raw files")
        pid = int(path.suffix[1:])
        try:
            programs = binding_program(raw, native, pid)
        except AssertionError as error:
            raise AssertionError(path.name + ": " + str(error)) from error
        matched = native_binding_lines(raw, native, library)
        require(matched, "native Nvim getpwuid binding to pinned library absent")
        native_proof = Path(case) / ("nss-native-" + str(pid) + ".json")
        native_raw = bounded(native_proof, 4096)
        total += len(native_raw)
        require(total <= 55 << 20, "NSS evidence exceeds bound; preserve all raw files")
        require(json.loads(native_raw) == {"pid": pid, "HOME": "/home/job",
                "cached_wrapper_enabled": True, "loader_nss_keys": [],
                "passwd": ["job", "x", 0, 0, "", "/home/job", "/bin/sh"]},
                "missing/changed native lookup proof after permanent NSS environment cleanup")
        evidence.append({"file": path.name, "bytes": len(raw), "sha256": digest(raw), "matched": matched,
                         "programs": programs, "native_init_file": native_proof.name,
                         "native_init_sha256": digest(native_raw)})
    producers = sorted(Path(case).glob("nss-producer-*.json"))
    require(0 < len(producers) <= 512, "missing/overlarge producer NSS boundary evidence")
    producer_evidence = []
    for path in producers:
        require(re.fullmatch(r"nss-producer-\d+\.json", path.name), "unexpected producer boundary artifact")
        raw_entry = bounded(path, 65536)
        entry = json.loads(raw_entry)
        maps = bounded(path.with_suffix(".maps"), 1 << 20)
        total += len(raw_entry) + len(maps)
        require(total <= 55 << 20, "NSS evidence exceeds bound; preserve all raw files")
        require(entry["loader_nss_keys"] == [] and entry["nss_library_mapped"] is False
                and b"libnss_wrapper" not in maps and entry["maps_sha256"] == digest(maps)
                and entry["command"] == [str(Path(job) / "bin/gopls")]
                and entry["identity"] == manifest["source"]["identity"], "producer NSS boundary proof differs")
        producer_evidence.append({"file": path.name, "sha256": digest(raw_entry),
                                  "maps_sha256": digest(maps)})
    write_json(Path(case) / "nss-binding-proof.json", {"native_sha256": NATIVE_SHA256,
               "library_sha256": LIBRARY_SHA256, "files": evidence, "bytes": total,
               "producer_boundaries": producer_evidence,
               "scope": "acquisition-control", "workflow_qualification": "requires-fresh-actual-run"})


def producer_boundary(log, command):
    """Before wire starts gopls: absence in exec environment and interpreter maps."""
    require_clean(os.environ)
    case = Path(log).parent
    if not (case / "nss-init.json").is_file():
        return
    require(case.is_absolute() and case.resolve() == case and case.parent.name == "c"
            and case.name in ("claude", "codex", "opencode"), "unowned producer evidence path")
    verify(case.parent.parent)
    with Path("/proc/self/maps").open("rb") as stream:
        maps = stream.read((1 << 20) + 1)
    require(len(maps) <= 1 << 20 and b"libnss_wrapper" not in maps,
            "producer relay mapped NSS library or maps exceed bound")
    prefix = case / ("nss-producer-" + str(os.getpid()))
    prefix.with_suffix(".maps").write_bytes(maps)
    write_json(prefix.with_suffix(".json"), {"pid": os.getpid(), "command": command,
               "loader_nss_keys": override_keys(os.environ), "nss_library_mapped": False,
               "maps_sha256": digest(maps), "identity": identity()})

