"""Bounded, fail-closed ownership proof for OpenCode 2.0.30's ACP HTTP IPC.

Caller contract: run inside the already guarded disposable scratch namespace; pass
the strace wrapper PID and the *native executable* path/hash from this run's
manifest. Capture after initialize and again immediately before stopping ACP.
Persist captures and raw traces. This is post-run evidence, not enforcement.

Use strace -ff -ttt -T -y -s 4096 -e trace=IPC_TRACE_SYSCALLS (no -qq).
Do not use -yy:
the proof needs socket:[inode] FD annotations, not decoded TCP descriptions.
The main destination audit must use syscall roles, retain failed attempts, and
independently require a real model-port connect. Only the exact original records
in allowed_connect_records may receive this exception.
"""
import ast
from decimal import Decimal, InvalidOperation
import hashlib
import os
from pathlib import Path
import re
import time

IPC_TRACE_SYSCALLS = (
    "network,process,close,close_range,dup,dup2,dup3,fcntl,unshare,setns"
)
PINNED_SHA256 = "3eb9af9b4ab07d4de85b68a2678a1e17534f9cfa833ade80b80831e51972da72"
MAX_PROCESSES = 256
MAX_TASKS = 2048
MAX_FDS = 4096
MAX_PROC_BYTES = 4 * 1024 * 1024
MAX_BINARY_BYTES = 512 * 1024 * 1024
MAX_TRACE_FILES = 4096
MAX_TRACE_BYTES = 128 * 1024 * 1024
MAX_TRACE_RECORDS = 500000
MAX_LINE_BYTES = 65536
NS = 1000000000


class EvidenceError(ValueError):
    """Missing, ambiguous, changed, or out-of-bounds evidence."""


def _need(condition, message):
    if not condition:
        raise EvidenceError(message)


def _bounded_bytes(path, limit):
    with open(path, "rb") as stream:
        value = stream.read(limit + 1)
    _need(len(value) <= limit, "metadata byte bound exceeded")
    return value


def _entries(path, limit):
    with os.scandir(path) as stream:
        result = []
        for entry in stream:
            _need(len(result) < limit, "directory entry bound exceeded")
            result.append(entry)
    return result


def _identity(pid):
    base = Path("/proc") / str(pid)
    raw = _bounded_bytes(base / "stat", 65536).decode("ascii")
    # comm may contain spaces and ')'; fields after its last ')' are numeric.
    fields = raw[raw.rfind(")") + 2:].split()
    _need(len(fields) >= 20, "short process stat")
    status = _bounded_bytes(base / "status", 65536).decode("ascii")
    uid = re.search(r"^Uid:\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)$", status, re.M)
    tgid = re.search(r"^Tgid:\s+(\d+)$", status, re.M)
    if uid is None or tgid is None:
        raise EvidenceError("missing process identity")
    _need(all(int(value) == os.getuid() for value in uid.groups()),
          "process UID differs from guarded job")
    return {
        "pid": int(pid), "tgid": int(tgid[1]), "ppid": int(fields[1]),
        "start_ticks": int(fields[19]), "uid": os.getuid(),
        "namespaces": {name: os.readlink(base / "ns" / name)
                       for name in ("net", "mnt", "user")},
    }


def _same_identity(before, after):
    _need(before == after, "process identity or namespace changed during capture")


def _sha256(path):
    size = 0
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        while True:
            chunk = stream.read(1024 * 1024)
            if not chunk:
                break
            size += len(chunk)
            _need(size <= MAX_BINARY_BYTES, "executable byte bound exceeded")
            digest.update(chunk)
    return digest.hexdigest()


def _signature(argv, expected_path):
    _need(argv and argv[0] == expected_path, "unexpected native argv[0]")
    if argv[1:] in (["acp"], ["--print-logs", "acp"]):
        return "acp"
    if argv[1:] == ["serve", "--stdio", "--port", "0"]:
        return "serve --stdio --port 0"
    raise EvidenceError("unexpected native argument signature")


def _native(pid, expected_path, expected_sha256):
    before = _identity(pid)
    base = Path("/proc") / str(pid)
    _need(os.readlink(base / "exe") == expected_path, "native executable path mismatch")
    exe_stat = (base / "exe").stat()
    raw = _bounded_bytes(base / "cmdline", 65536)
    _need(raw.endswith(b"\0"), "unterminated native argv")
    argv = [part.decode("utf-8", errors="strict") for part in raw[:-1].split(b"\0")]
    signature = _signature(argv, expected_path)
    digest = _sha256(base / "exe")
    _need(digest == expected_sha256, "native executable hash mismatch")
    after_stat = (base / "exe").stat()
    _need((exe_stat.st_dev, exe_stat.st_ino, exe_stat.st_size, exe_stat.st_mtime_ns)
          == (after_stat.st_dev, after_stat.st_ino, after_stat.st_size, after_stat.st_mtime_ns),
          "native executable changed while hashing")
    _same_identity(before, _identity(pid))
    # Never return arbitrary argv, environment, credentials, or file contents.
    return {
        "identity": before, "executable": expected_path, "sha256": digest,
        "exe_device": exe_stat.st_dev, "exe_inode": exe_stat.st_ino,
        "signature": signature,
    }


def _descendants(root_pid, namespaces):
    """Walk only this root's children, including children created by its TIDs."""
    found = {}
    tasks = {}
    pending = [root_pid]
    count = 0
    while pending:
        pid = pending.pop()
        _need(pid not in found, "ambiguous descendant ancestry")
        _need(len(found) < MAX_PROCESSES, "process bound exceeded")
        identity = _identity(pid)
        _need(identity["tgid"] == pid, "expected process leader")
        _need(identity["namespaces"] == namespaces, "descendant namespace differs from job")
        found[pid] = identity
        process_tasks = []
        for entry in _entries(Path("/proc") / str(pid) / "task", MAX_TASKS):
            _need(entry.name.isdigit(), "unexpected task entry")
            count += 1
            _need(count <= MAX_TASKS, "task bound exceeded")
            tid = int(entry.name)
            task = _identity(tid)
            _need(task["tgid"] == pid and task["namespaces"] == namespaces,
                  "task ownership mismatch")
            process_tasks.append(task)
            children = _bounded_bytes(
                Path("/proc") / str(pid) / "task" / str(tid) / "children", 65536
            ).split()
            for child in children:
                _need(child.isdigit(), "invalid child PID")
                child_pid = int(child)
                child_identity = _identity(child_pid)
                _need(child_identity["ppid"] == pid, "child ancestry changed")
                pending.append(child_pid)
        tasks[pid] = sorted(process_tasks, key=lambda task: task["pid"])
        _same_identity(identity, _identity(pid))
    return found, tasks


def _ancestry(pid, root_pid, identities):
    chain = []
    while True:
        _need(pid in identities and pid not in chain, "unproven process ancestry")
        chain.append(pid)
        if pid == root_pid:
            return list(reversed(chain))
        pid = identities[pid]["ppid"]


def _owned_listener(server_pid, model_port):
    base = Path("/proc") / str(server_pid)
    inodes = {}
    for entry in _entries(base / "fd", MAX_FDS):
        _need(entry.name.isdigit(), "invalid FD entry")
        link = os.readlink(entry.path)
        match = re.fullmatch(r"socket:\[(\d+)\]", link)
        if match:
            inodes.setdefault(int(match[1]), []).append(int(entry.name))
    listeners = []
    namespace_listeners = []
    for table in ("tcp", "tcp6"):
        raw = _bounded_bytes(base / "net" / table, MAX_PROC_BYTES).decode("ascii")
        lines = raw.splitlines()
        _need(lines and "local_address" in lines[0], "invalid TCP table header")
        _need(len(lines) <= 32768, "TCP table record bound exceeded")
        for line in lines[1:]:
            fields = line.split()
            _need(len(fields) >= 10, "short TCP table record")
            inode = int(fields[9])
            if fields[3] != "0A":
                continue
            address, port = fields[1].split(":")
            namespace_listeners.append((table, address, int(port, 16), inode))
            if inode not in inodes:
                continue
            _need(int(fields[7]) == os.getuid(), "listener UID differs from job")
            _need(table == "tcp" and address == "0100007F",
                  "owned listener is not exact IPv4 127.0.0.1")
            _need(len(inodes[inode]) == 1, "listener has ambiguous FD aliases")
            listeners.append({"inode": inode, "fd": inodes[inode][0],
                              "address": "127.0.0.1", "port": int(port, 16)})
    _need(len(listeners) == 1, "expected exactly one owned TCP listener")
    listener = listeners[0]
    # A different inode at the same port could receive the ACP connection via
    # SO_REUSEPORT (including wildcard/IPv6 listeners). Ownership alone is not
    # enough: conservatively require one listener at this port in the namespace.
    at_port = [item for item in namespace_listeners if item[2] == listener["port"]]
    _need(at_port == [("tcp", "0100007F", listener["port"], listener["inode"])],
          "ambiguous control listener in network namespace")
    _need(0 < listener["port"] <= 65535 and listener["port"] != model_port,
          "invalid control port or control/model port collision")
    return listener


def capture_acp_listener(root_pid, expected_binary_path, expected_sha256, model_port):
    """Return JSON evidence; any error is a violation and cannot authorize IPC."""
    report = {"version": 1, "violations": [], "begin_ns": time.time_ns(),
              "root_pid": root_pid, "model_port": model_port}
    try:
        _need(isinstance(root_pid, int) and root_pid > 0, "invalid tracing wrapper PID")
        _need(isinstance(model_port, int) and 0 < model_port <= 65535, "invalid model port")
        _need(expected_sha256 == PINNED_SHA256, "manifest is not the pinned OpenCode 2.0.30")
        expected_path = str(Path(expected_binary_path).resolve(strict=True))
        _need(Path(expected_binary_path).is_absolute(), "manifest executable path must be absolute")
        _need(_sha256(expected_path) == expected_sha256, "manifest executable hash mismatch")
        report["expected_binary_path"] = expected_path
        report["expected_sha256"] = expected_sha256
        own = _identity(os.getpid())
        identities, tasks = _descendants(root_pid, own["namespaces"])
        report["root"] = identities[root_pid]
        native = []
        for pid in identities:
            if os.readlink(Path("/proc") / str(pid) / "exe") == expected_path:
                native.append(_native(pid, expected_path, expected_sha256))
        acps = [item for item in native if item["signature"] == "acp"]
        servers = [item for item in native if item["signature"] == "serve --stdio --port 0"]
        _need(len(acps) == 1 and len(servers) == 1, "ambiguous native ACP/server processes")
        acp, server = acps[0], servers[0]
        acp_pid, server_pid = acp["identity"]["pid"], server["identity"]["pid"]
        acp_chain = _ancestry(acp_pid, root_pid, identities)
        server_chain = _ancestry(server_pid, root_pid, identities)
        _need(root_pid != acp_pid and acp_pid in server_chain[:-1],
              "native server is not beneath traced ACP")
        listener = _owned_listener(server_pid, model_port)
        # Re-read executable, argv, hash, and all necessary ancestry identities.
        _need(acp == _native(acp_pid, expected_path, expected_sha256),
              "ACP changed during ownership capture")
        _need(server == _native(server_pid, expected_path, expected_sha256),
              "server changed during ownership capture")
        for pid in set(acp_chain + server_chain):
            _same_identity(identities[pid], _identity(pid))
        _need(listener == _owned_listener(server_pid, model_port),
              "listener changed during ownership capture")
        report.update(acp=acp, server=server, listener=listener,
                      ancestry=[identities[pid] for pid in server_chain],
                      acp_tasks=tasks[acp_pid], server_tasks=tasks[server_pid],
                      clock_ticks=os.sysconf("SC_CLK_TCK"),
                      boot_realtime_ns=time.time_ns() - time.clock_gettime_ns(time.CLOCK_BOOTTIME))
    except (OSError, ValueError, UnicodeError, KeyError, IndexError) as error:
        report["violations"].append("ownership capture: " + str(error))
    report["end_ns"] = time.time_ns()
    return report


def _ns(value):
    try:
        result = Decimal(value) * NS
        _need(result.is_finite() and result == int(result), "unsupported timestamp precision")
        return int(result)
    except (InvalidOperation, OverflowError) as error:
        raise EvidenceError("invalid trace timestamp") from error


def _split(value):
    """Split strace's top-level arguments, never interpreting quoted payloads."""
    result, start, stack = [], 0, []
    quoted = escaped = False
    pairs = {")": "(", "]": "[", "}": "{"}
    for index, char in enumerate(value):
        if quoted:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                quoted = False
        elif char == '"':
            quoted = True
        elif char in "([{":
            stack.append(char)
        elif char in ")]}":
            _need(stack and stack.pop() == pairs[char], "unbalanced syscall arguments")
        elif char == "," and not stack:
            result.append(value[start:index].strip())
            start = index + 1
    _need(not quoted and not stack, "truncated syscall arguments")
    result.append(value[start:].strip())
    return result


def destination_text(record):
    """Return only explicit destination sockaddr text, never bind/HTTP payloads.

    NULL send destinations use a previously connected socket and have no new
    address to authorize. Unknown destination shapes raise EvidenceError.
    """
    call, args = record.get("call"), record.get("args", [])
    if call in ("sendmmsg", "socketcall"):
        raise EvidenceError("unsupported outbound syscall: " + call)
    if call not in ("connect", "sendto", "sendmsg"):
        return None
    if call == "connect":
        _need(len(args) == 3, "unparsed connect arguments")
        return args[1]
    if call == "sendto":
        _need(len(args) == 6, "unparsed sendto arguments")
        return None if args[4] == "NULL" else args[4]
    _need(len(args) == 3 and args[1].startswith("{") and args[1].endswith("}"),
          "unparsed sendmsg arguments")
    names = [part[len("msg_name="):] for part in _split(args[1][1:-1])
             if part.startswith("msg_name=")]
    _need(len(names) == 1, "missing or ambiguous sendmsg destination")
    return None if names[0] == "NULL" else names[0]


def _ipv4(sockaddr):
    if sockaddr is None or not sockaddr.startswith("{") or not sockaddr.endswith("}"):
        return None
    fields = _split(sockaddr[1:-1])
    if len(fields) != 3 or fields[0] != "sa_family=AF_INET":
        return None
    port = re.fullmatch(r"sin_port=htons\((\d+)\)", fields[1])
    address = re.fullmatch(r'sin_addr=inet_addr\("([0-9.]+)"\)', fields[2])
    if not port or not address:
        return None
    return address[1], int(port[1])


def _record(tid, path, original, start, end, text, complete):
    match = re.fullmatch(r"(\w+|\?\?\?)\((.*)\)\s+=\s+(.+)", text)
    if match is None:
        raise EvidenceError("unparsed syscall record")
    call, arguments, result = match.groups()
    _need(call != "???" or (not complete and not arguments.strip() and result == "?"),
          "unknown syscall outside terminal killed record")
    _need(complete or result == "?", "unexpected incomplete syscall result")
    duration = re.search(r"\s+<(\d+\.\d+)>$", result)
    if duration is None:
        # No-return exits and verified terminal interrupted calls have no -T.
        _need(result == "?" and (call in ("exit", "exit_group") or not complete),
              "missing -T syscall duration")
    else:
        end = max(end, start + _ns(duration[1]))
        result = result[:duration.start()].rstrip()
    record = {"tid": tid, "path": path, "start_ns": start, "end_ns": end,
              "call": call, "args": [] if call == "???" else _split(arguments), "result": result,
              "text": text, "original_records": original, "complete": complete}
    # Validate destination structure here, even for failed attempts.
    destination_text(record)
    return record


def read_trace_records(case):
    """Bounded parser shared with the main audit.

    Returns {records, violations}. A syscall record includes call, args, result,
    start_ns/end_ns, tid, path, and original_records=[{path,line}]. Split syscalls
    retain *both* original lines. Signals/exits have kind and no call. A terminal
    unfinished call immediately followed by the same TID's final exit is retained
    with complete=False; unknown ??? is accepted only when that exit is a kill.
    Its recoverable explicit destination is still validated. Other missing timing,
    orphaned resumes, unfinished calls and truncation fail closed. Caller must
    preserve parser violations in its main audit too.
    """
    report = {"records": [], "violations": []}
    total = 0
    try:
        files = []
        for entry in _entries(case, MAX_TRACE_FILES + 1024):
            if entry.name.startswith("audit-"):
                _need(entry.is_file(follow_symlinks=False), "trace is not a regular file")
                _need(re.fullmatch(r"audit-[A-Za-z0-9_-]+\.\d+", entry.name) is not None,
                      "trace filename lacks numeric TID")
                files.append(Path(entry.path))
        _need(files and len(files) <= MAX_TRACE_FILES, "missing traces or trace file bound exceeded")
        for path in sorted(files):
            tid = int(path.name.rsplit(".", 1)[1])
            pending = None
            pending_separated = False
            terminated = False
            previous = -1
            with path.open("rb") as stream:
                while True:
                    raw = stream.readline(MAX_LINE_BYTES + 1)
                    if not raw:
                        break
                    _need(not terminated, "trace record after TID exit")
                    total += len(raw)
                    _need(total <= MAX_TRACE_BYTES, "trace byte bound exceeded")
                    _need(len(raw) <= MAX_LINE_BYTES and raw.endswith(b"\n"),
                          "oversized or incomplete trace line")
                    line = raw[:-1].decode("utf-8", errors="strict")
                    match = re.fullmatch(r"(\d+\.\d+)\s+(.+)", line)
                    if match is None:
                        raise EvidenceError("missing -ttt timestamp or malformed trace")
                    stamp, body = _ns(match[1]), match[2]
                    _need(stamp >= previous, "nonmonotonic TID trace")
                    previous = stamp
                    original = {"path": path.name, "line": line}
                    _need(len(report["records"]) < MAX_TRACE_RECORDS, "trace record bound exceeded")
                    if body.startswith("--- ") and body.endswith(" ---"):
                        pending_separated = pending is not None
                        report["records"].append({"kind": "signal", "tid": tid, "path": path.name,
                                                  "start_ns": stamp, "end_ns": stamp,
                                                  "original_records": [original]})
                        continue
                    if body.startswith("+++ ") and body.endswith(" +++"):
                        _need(re.fullmatch(r"\+\+\+ (exited with \d+|killed by SIG\w+(?: \(core dumped\))?) \+\+\+", body),
                              "unrecognized trace terminal record")
                        if pending is not None:
                            _need(not pending_separated, "terminal syscall not immediately before exit")
                            start, prefix, first, terminal_only = pending
                            _need(not prefix.startswith("???(") or body.startswith("+++ killed by "),
                                  "unknown terminal syscall without killed exit")
                            report["records"].append(_record(
                                tid, path.name, [first], start, stamp,
                                prefix.rstrip() + ") = ?", False))
                            pending = None
                            _need(len(report["records"]) < MAX_TRACE_RECORDS,
                                  "trace record bound exceeded")
                        report["records"].append({"kind": "exit", "tid": tid, "path": path.name,
                                                  "start_ns": stamp, "end_ns": stamp,
                                                  "original_records": [original]})
                        terminated = True
                        continue
                    terminal = re.fullmatch(r"(.*)<unfinished \.\.\.>\)\s+=\s+\?", body)
                    if body.endswith("<unfinished ...>") or terminal:
                        _need(pending is None, "overlapping unfinished syscalls")
                        prefix = terminal[1] if terminal else body[:-len("<unfinished ...>")]
                        _need(re.match(r"(?:\w+|\?\?\?)\(", prefix) is not None,
                              "bad unfinished syscall")
                        _need(not prefix.startswith("???(") or (not terminal and prefix.strip() == "???("),
                              "unrecognized unknown terminal syscall")
                        pending = (stamp, prefix, original, terminal is not None)
                        pending_separated = False
                        continue
                    resumed = re.fullmatch(r"<\.\.\. (\w+) resumed>(.*)", body)
                    if resumed:
                        if pending is None:
                            raise EvidenceError("orphan resumed syscall")
                        start, prefix, first, terminal_only = pending
                        _need(not terminal_only, "terminal syscall was resumed")
                        _need(prefix.startswith(resumed[1] + "("), "resumed syscall mismatch")
                        text = prefix + resumed[2]
                        originals = [first, original]
                        pending = None
                    else:
                        _need(pending is None, "unfinished syscall was not resumed")
                        start, text, originals = stamp, body, [original]
                    report["records"].append(_record(tid, path.name, originals, start, stamp, text, True))
            _need(pending is None, "unfinished syscall at end of trace")
        report["records"].sort(key=lambda item: (item["start_ns"], item["path"]))
    except (OSError, ValueError, UnicodeError) as error:
        report["violations"].append("trace evidence: " + str(error))
    return report


def _return(record):
    match = re.match(r"(-?\d+)(?:\s|<|$)", record.get("result", ""))
    if match is None:
        raise EvidenceError("unknown syscall result")
    return int(match[1])


def _fd(argument):
    match = re.fullmatch(r"(\d+)(?:<([^>]*)>)?", argument)
    if match is None:
        raise EvidenceError("unparsed file descriptor")
    inode = re.fullmatch(r"socket:\[(\d+)\]", match[2] or "")
    return int(match[1]), int(inode[1]) if inode else None


def _exec_signature(record, expected_path):
    args = record["args"]
    _need(record["call"] == "execve" and len(args) == 3, "missing native execve")
    try:
        executable = ast.literal_eval(args[0])
        argv = ast.literal_eval(args[1])
    except (SyntaxError, ValueError) as error:
        raise EvidenceError("unparsed native execve arguments") from error
    _need(executable == expected_path and isinstance(argv, list)
          and all(isinstance(part, str) for part in argv), "native execve path mismatch")
    return _signature(argv, expected_path)


def _trace_tree(records, acp_pid):
    """Resolve clone TIDs to their process leaders, independent of file ordering."""
    groups = {acp_pid: acp_pid}
    parents = {}
    births = {}
    clones = [item for item in records
              if item.get("complete") and item.get("call") in ("clone", "clone3", "fork", "vfork")
              and _return(item) > 0]
    _need(len(clones) <= MAX_TASKS, "trace task bound exceeded")
    pending = list(clones)
    while pending:
        remaining = []
        progress = False
        for item in pending:
            parent = item["tid"]
            if parent not in groups:
                remaining.append(item)
                continue
            child = _return(item)
            _need(child not in groups, "reused or ambiguous trace PID/TID")
            thread = "CLONE_THREAD" in ",".join(item["args"])
            if thread:
                _need("CLONE_FILES" in ",".join(item["args"]), "thread has unshared file table")
            groups[child] = groups[parent] if thread else child
            parents[child] = parent
            births[child] = item
            progress = True
        if not progress:
            break
        pending = remaining
    return groups, parents, births


def _trace_after_birth(record, births, acp_exec):
    birth = births.get(record["tid"], acp_exec)
    _need(record["start_ns"] >= birth["start_ns"], "trace activity predates task birth")


def _capture_pair(initial, final, model_port):
    _need(isinstance(initial, dict) and isinstance(final, dict), "missing live ownership captures")
    for capture in (initial, final):
        _need(capture.get("version") == 1 and not capture.get("violations"),
              "live ownership capture failed")
        _need(capture["model_port"] == model_port, "capture model port mismatch")
        _need(capture["begin_ns"] <= capture["end_ns"], "invalid capture interval")
        _need(capture["expected_sha256"] == PINNED_SHA256, "capture binary is not pinned")
    _need(initial["end_ns"] < final["begin_ns"], "ownership captures overlap or are reversed")
    for key in ("root_pid", "root", "acp", "server", "listener", "ancestry",
                "expected_binary_path", "expected_sha256", "clock_ticks"):
        _need(initial[key] == final[key], "ownership changed between captures: " + key)
    _need(abs(initial["boot_realtime_ns"] - final["boot_realtime_ns"]) <= 50000000,
          "clock changed between ownership captures")
    listener = initial["listener"]
    _need(listener["address"] == "127.0.0.1" and 0 < listener["port"] <= 65535
          and listener["port"] != model_port, "capture endpoint is not eligible")
    return listener


def audit_acp_ipc(case, model_port, initial_capture, final_capture):
    """Authorize only exact client connect records after live + temporal proof.

    Every violation clears the entire exception. Other destination policy,
    including service traces, discovery traffic and Unix sockets, belongs to the
    main audit and is never relaxed here.
    """
    proof = {"version": 1, "allowed_connect_records": [], "violations": [],
             "model_port": model_port}
    try:
        listener = _capture_pair(initial_capture, final_capture, model_port)
        parsed = read_trace_records(case)
        _need(not parsed["violations"], "; ".join(parsed["violations"]))
        client = [item for item in parsed["records"]
                  if re.fullmatch(r"audit-client\.\d+", item["path"])]
        _need(client, "missing traced client tree")
        acp_pid = initial_capture["acp"]["identity"]["pid"]
        server_pid = initial_capture["server"]["identity"]["pid"]
        expected_path = initial_capture["expected_binary_path"]
        acp_execs = [item for item in client if item["tid"] == acp_pid
                     and item.get("complete") and item.get("call") == "execve" and _return(item) == 0]
        _need(len(acp_execs) == 1 and _exec_signature(acp_execs[0], expected_path) == "acp",
              "missing or ambiguous ACP native execve")
        acp_exec = acp_execs[0]
        groups, parents, births = _trace_tree(client, acp_pid)
        _need(groups.get(server_pid) == server_pid and server_pid in parents,
              "server is not in traced ACP descendants")
        server_execs = [item for item in client if item["tid"] == server_pid
                        and item.get("complete") and item.get("call") == "execve" and _return(item) == 0]
        _need(len(server_execs) == 1
              and _exec_signature(server_execs[0], expected_path) == "serve --stdio --port 0",
              "missing or ambiguous native server execve")
        server_exec = server_execs[0]
        for capture in (initial_capture, final_capture):
            for role, execution in (("acp", acp_exec), ("server", server_exec)):
                identity = capture[role]["identity"]
                start_ns = (capture["boot_realtime_ns"]
                            + identity["start_ticks"] * NS // capture["clock_ticks"])
                # /proc starttime is tick-granular. Allow two ticks of rounding.
                slack = 2 * NS // capture["clock_ticks"]
                _need(execution["start_ns"] + slack >= start_ns
                      and execution["end_ns"] <= initial_capture["begin_ns"],
                      "native execve does not match live process lifetime")
                if identity["pid"] in births:
                    birth = births[identity["pid"]]
                    _need(birth["start_ns"] - slack <= start_ns <= birth["end_ns"] + slack,
                          "trace creation does not match live process starttime")
            for task in capture["acp_tasks"] + capture["server_tasks"]:
                _need(groups.get(task["pid"]) == task["tgid"],
                      "live TID lacks traced thread ancestry")
                if task["pid"] in births:
                    birth = births[task["pid"]]
                    start_ns = (capture["boot_realtime_ns"]
                                + task["start_ticks"] * NS // capture["clock_ticks"])
                    slack = 2 * NS // capture["clock_ticks"]
                    _need(birth["start_ns"] - slack <= start_ns <= birth["end_ns"] + slack,
                          "trace TID creation does not match live task starttime")
        # Verify the process ancestry, allowing thread IDs in clone parentage.
        ancestry = initial_capture["ancestry"]
        acp_index = next(index for index, identity in enumerate(ancestry)
                         if identity["pid"] == acp_pid)
        for identity in ancestry[acp_index + 1:]:
            pid = identity["pid"]
            _need(pid in parents and groups.get(parents[pid]) == identity["ppid"],
                  "live ancestry disagrees with trace creation")

        connects = []
        for item in client:
            if item.get("call") != "connect":
                continue
            if _ipv4(destination_text(item)) == (listener["address"], listener["port"]):
                _need(item.get("complete"), "incomplete control connect cannot prove ownership")
                _need(groups.get(item["tid"]) == acp_pid,
                      "control connect outside native ACP thread group")
                _trace_after_birth(item, births, acp_exec)
                connects.append(item)
        _need(connects, "no ACP control connect captured")
        first = min(item["start_ns"] for item in connects)
        last = max(item["end_ns"] for item in connects)
        _need(last <= final_capture["begin_ns"], "control connection after final live capture")

        owned = [item for item in client if groups.get(item["tid"]) == server_pid]
        fd, inode = listener["fd"], listener["inode"]
        # Snapshot uniqueness alone cannot exclude a transient SO_REUSEPORT peer
        # between captures. Refuse successful/unknown use of this option on the
        # listener, even before bind; do not infer safety from an option payload.
        for item in client:
            if item.get("call") != "setsockopt" or item["start_ns"] > last:
                continue
            args = item["args"]
            _need(len(args) >= 3, "short setsockopt syscall")
            if args[1:3] != ["SOL_SOCKET", "SO_REUSEPORT"]:
                continue
            number, socket_inode = _fd(args[0])
            if socket_inode == inode or (groups.get(item["tid"]) == server_pid and number == fd):
                _need(item.get("complete") and _return(item) < 0,
                      "listener SO_REUSEPORT use is not safe to authorize")
        binds, listens, names = [], [], []
        for item in owned:
            call = item.get("call")
            if call not in ("bind", "listen", "getsockname"):
                continue
            args = item["args"]
            _need(len(args) >= 2, "short listener lifecycle syscall")
            number, socket_inode = _fd(args[0])
            if socket_inode != inode:
                continue
            _trace_after_birth(item, births, acp_exec)
            _need(item.get("complete"), "incomplete listener lifecycle cannot prove ownership")
            _need(number == fd, "listener changed FD")
            _need(_return(item) == 0, "listener lifecycle operation failed")
            if call == "bind":
                _need(len(args) == 3 and _ipv4(args[1]) == ("127.0.0.1", 0),
                      "listener was not bound to exact loopback port zero")
                binds.append(item)
            elif call == "getsockname":
                _need(len(args) == 3
                      and _ipv4(args[1]) == ("127.0.0.1", listener["port"]),
                      "assigned listener endpoint does not match live evidence")
                names.append(item)
            else:
                listens.append(item)
        _need(len(binds) == len(listens) == 1 and names, "missing or ambiguous listener lifecycle")
        bind, listen = binds[0], listens[0]
        _need(server_exec["end_ns"] <= bind["start_ns"]
              and bind["end_ns"] <= listen["start_ns"]
              and listen["end_ns"] < first
              and any(bind["end_ns"] <= item["start_ns"] and item["end_ns"] < first
                      for item in names), "listener/assigned port not established before all connects")
        _need(listen["end_ns"] <= initial_capture["begin_ns"],
              "listener lifecycle starts after initial live capture")

        # Reject closure, FD replacement, table changes or process exit during the
        # entire proven interval. Server TIDs share the listener's file table.
        for item in owned:
            if item["end_ns"] < bind["start_ns"] or item["start_ns"] > last:
                continue
            call, args = item.get("call"), item.get("args", [])
            _need(not call or item.get("complete"), "incomplete server syscall during ownership interval")
            if item.get("kind") == "exit":
                _need(item["tid"] != server_pid, "server exited before last control connect")
                continue
            if call in ("exit", "exit_group"):
                _need(call != "exit_group" and item["tid"] != server_pid,
                      "server exited before last control connect")
                continue
            if not call:
                continue
            if call in ("execve", "execveat", "setns", "unshare") and _return(item) == 0:
                raise EvidenceError("server executable or namespace/file table changed")
            if call == "close":
                # Linux may release an FD even when close reports an error.
                number, socket_inode = _fd(args[0])
                _need(number != fd and socket_inode != inode,
                      "listener closed before last control connect")
            elif call == "close_range" and _return(item) == 0:
                low = int(args[0])
                high = (1 << 32) - 1 if args[1] == "~0U" else int(args[1])
                _need(not low <= fd <= high, "close_range affected listener")
            elif call in ("dup", "dup2", "dup3") and _return(item) >= 0:
                source, source_inode = _fd(args[0])
                target = _fd(args[1])[0] if call != "dup" else _return(item)
                _need(source != fd and source_inode != inode and target != fd,
                      "listener duplicated or replaced")
            elif (call == "fcntl" and len(args) >= 2
                  and args[1] in ("F_DUPFD", "F_DUPFD_CLOEXEC") and _return(item) >= 0):
                number, socket_inode = _fd(args[0])
                _need(number != fd and socket_inode != inode and _return(item) != fd,
                      "listener duplicated with fcntl")
            elif call == "shutdown" and _return(item) == 0:
                number, socket_inode = _fd(args[0])
                _need(number != fd and socket_inode != inode, "listener shut down")
            elif call in ("clone", "clone3") and _return(item) > 0:
                flags = ",".join(args)
                _need("CLONE_FILES" not in flags or "CLONE_THREAD" in flags,
                      "listener file table shared outside server thread group")
        # A new socket result with this FD after bind would be an unobserved
        # close/replacement. Annotation mismatches also invalidate the proof.
        for item in owned:
            if not bind["end_ns"] <= item["start_ns"] <= last:
                continue
            if item.get("call") == "socket" and _return(item) == fd:
                raise EvidenceError("listener FD was reused")
            if item.get("call") in ("bind", "listen", "getsockname", "close", "shutdown"):
                number, socket_inode = _fd(item["args"][0])
                if number == fd:
                    _need(socket_inode == inode, "listener FD annotation missing or changed")
        proof.update(endpoint=listener, acp_pid=acp_pid, server_pid=server_pid,
                     first_connect_ns=first, last_connect_ns=last,
                     listener_records=[entry for item in [bind, listen, *names]
                                       for entry in item["original_records"]])
        seen = set()
        for item in connects:
            for entry in item["original_records"]:
                key = (entry["path"], entry["line"])
                _need(key not in seen, "ambiguous duplicate connect trace record")
                seen.add(key)
                proof["allowed_connect_records"].append(entry)
    except (OSError, ValueError, KeyError, TypeError, IndexError, StopIteration) as error:
        proof["violations"].append("ACP IPC proof: " + str(error))
        proof["allowed_connect_records"] = []
    return proof
