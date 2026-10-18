# M9 Poetry interpreter query budget

## Observed failure and bounded measurement

At source 0bdcb74c6c11d93901d4991ef48f6fbe36a96f26, M9 case08
(poetry_external) in root-owned agent-a job af01cb69 reported
environment_manager_query_failed. The real selected Poetry venv passed both
ordinary and isolated Python import checks, and fixture env use and
env info --path succeeded. The production query
poetry --no-plugins env info --executable executed and was killed by SIGKILL;
its selected-venv Python base-prefix child exited successfully. That original
trace did not capture elapsed duration or stderr, so the kill alone did not
establish deadline expiry.

Root-owned standalone agent-a job 7edc8e0b reproduced the same actual query,
query-relevant fixture and explicit environment with private Poetry 2.2.1,
CPython 3.12.8 and uv 0.9.5. Provisioning reused the exact Python/uv/Poetry block
from accepted tests/m9-language-probe/setup.sh, its dependency cutoff
2026-10-16T00:00:00Z, source-owned safety guards, and the accepted private
CPython/strace/libunwind archive pins. No Huyang candidate build or language server
was needed. The root verified the source tree and five entrypoint blobs, checked
the exact transported wrapper hash, and ran remote shell syntax validation.

The job exited 0 at measurement-complete; the complete returned archive was
10,519 bytes. The four samples ran sequentially in the order shown:

| Query mode | Budget | Elapsed seconds | Return code | Deadline expired | Stdout bytes | Stderr bytes |
| --- | --- | --- | --- | --- | --- | --- |
| Untraced | 2 seconds | 2.006309508 | -9 (SIGKILL) | yes | 0 | 0 |
| Untraced | 10 seconds | 2.318742187 | 0 | no | 77 | 0 |
| Exec-only traced | 2 seconds | 1.744214275 | 0 | no | 77 | 0 |
| Exec-only traced | 10 seconds | 1.754777353 | 0 | no | 77 | 0 |

All successful outputs named the correct selected venv interpreter. No output
overflow occurred. Tracing used the acceptance probe's
--kill-on-exit --seccomp-bpf -ff -e trace=execve,execveat -s 2048 options.
The query timer ran inside the traced harness, excluding strace startup.

These are four ordered samples with warm-cache and ordering limitations. They
demonstrate that a legitimate **untraced** query can need more than two seconds;
they do not establish trace causality, cold-start performance, a latency
distribution, or the exact sender of the original production SIGKILL.
The standalone driver captured bounded stderr for diagnosis and explicitly
cleaned up the query process group; those details differ from the production
runner. This measurement is not production acceptance of the candidate change.

Root's retained evidence is under
~/.cache/huyang-friction/executor/m9-poetry-measurement-artifacts/, including
outcome.json, r1/08/measurements/, raw stdout/stderr, validation records,
r1/08/exec.*, package freeze, provisioning pins and exact setup block.
The prepared wrapper and handoff are under
~/.cache/huyang-friction/m9-poetry-diagnosis/.

## Candidate behavior and responsiveness

Poetry discovery now has the named finite budget poetryPythonQueryTimeout of
five seconds. This provides headroom over the observed 2.319-second successful
untraced query without adopting the ten-second measurement ceiling. uv retains
its two-second budget, named uvPythonQueryTimeout.

Discovery still uses context.WithTimeout(caller, budget), preserving an earlier
caller deadline and pre-existing cancellation. Query arguments, trust and
canonicalization checks, environment precedence, venv-marker requirements,
8192-byte stdout limit, 100ms WaitDelay and discarded public stderr are unchanged.
There is no retry, dependency installation or relaxed base-interpreter acceptance.

The embed provider currently calls discovery with context.Background() in
internal/provider/embed/embed.go. Consequently Poetry discovery in its
background bootstrap can now last up to five seconds, plus bounded subprocess
cleanup. The outer bootstrap remains cancellable as before; its cancellation
does not propagate into that background discovery. This change does not modify
provider lifecycle or broaden that existing context relationship.

## Coverage and validation boundary

TestProjectPythonManagerQueries checks the expected finite manager budget using
timestamps immediately before resolution and inside the injected query, while
retaining exact command, arguments, cwd and successful symlinked-venv selection
assertions. It does not wait for expiry.

TestProjectPythonManagerCallerContext covers uv and Poetry with an earlier
caller deadline and an already-canceled caller. The earlier deadline must survive
unchanged; cancellation must already be visible in the injected query context.
Injected errors must produce unresolved discovery with
environment_manager_query_failed. No sleeps or real manager executions are
used in these unit tests.

The implementation child ran no build, test, lint, formatter, generator,
provisioning, service or acceptance command. Root must verify the committed
candidate revision and its integrated revision separately. Suggested focused
command inside the root's guarded scratch-HOME agent-a job:

    HUYANG_FRICTION=0 go test ./internal/workspace -run '^(TestProjectPython.*|TestPythonManagerOutputIsBounded)$' -count=1

The root owns required broader checks, independent review and a real production
case08 run after integration. The source0bd standalone measurement does not
claim those candidate checks have passed.
