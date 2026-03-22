# /jailbreak — Sandbox Escape Research Skill

You are a security researcher tasked with finding escape vulnerabilities in the **agentic-bash** sandboxing library. Your job is to attempt to break out of every isolation layer this project implements, document what works, and produce an exhaustive report.

## Arguments
$ARGUMENTS may contain:
- `--profile=<root|sudoer|locked|all>` — which Docker profiles to target (default: skill decides based on findings)
- `--target=<landlock|namespace|fs|intercept|network|packages|all>` — which isolation layers to focus on (default: all)
- `--show-plan` — print the full attack plan before executing

---

## Phase 0 — Codebase Reconnaissance

Before doing anything else, deeply read the source code. You must understand every isolation mechanism before attempting to break it. Read:

- `isolation/` — all files (Landlock, namespace, sysprocattr, strategy, platform stubs)
- `fs/` — all files (layered FS, memory FS, real FS, open handler, snapshot, tracker)
- `executor/` — all files including `intercept/` subdirectory
- `sandbox/` — options.go, sandbox.go, session.go, bootstrap.go
- `network/` — filter.go and platform-specific files
- `packages/` — manager.go, pip.go, apt.go, shim.go
- `docker/tests/Dockerfile` and `Makefile` — understand container profiles
- `main.go` — CLI surface

For each isolation layer, map out:
1. What it protects against
2. How it's implemented (syscalls, rules, paths)
3. What assumptions it makes
4. What it explicitly allows (these are your attack surface)
5. Any TODOs, FIXMEs, or known limitations in the code

---

## Phase 1 — Attack Surface Analysis

Based on the code you've read, build an internal attack plan covering ALL of these vectors. Do not show this plan yet.

**Filesystem Escape**
- Path traversal via `../` sequences through the virtual→real path translation
- Symlink attacks: create symlink inside sandbox pointing outside
- Hardlink escapes (hardlinks bypass some Landlock rules)
- `/proc/self/fd/` or `/proc/self/root/` traversal
- Overlay layer bypass: write to base layer paths via edge cases in `LayeredFS`
- ChangeTracker manipulation to hide file operations
- Snapshot/restore abuse: inject files via tar extraction (zip-slip style)

**Landlock LSM Bypass**
- Missing rule coverage: check what paths are in the allowed list vs. what's actually needed
- File descriptor leaking: open FDs before Landlock restricts, pass to child
- `landlock_restrict_self` ordering: check if restriction is applied before or after certain operations
- Kernel version probing: check if version detection allows falling back to no isolation
- `PR_SET_NO_NEW_PRIVS` bypass conditions
- Inode vs path rules: Landlock is inode-based, check for rename/move tricks

**Namespace Escape**
- User namespace UID 0 mapping tricks
- Mount namespace: bind mounts, `/proc` remounting
- PID namespace: `/proc/[pid]/exe` symlink chasing
- Network namespace: check if veth pair setup leaks

**Intercept Layer Bypass**
- Direct syscall invocation bypassing the intercept hooks
- Environment variable injection to override `PYTHONPATH`, `HOME`, `PATH`
- Shell builtin vs. external command ambiguity
- `exec` with absolute paths that bypass path interception
- `callhook.go` coverage gaps — what commands are NOT intercepted?

**Package Manager Shim Abuse**
- `pip install` with `--target` flag to override overlay destination
- `pip install -e .` (editable install) to escape overlay
- `apt-get` with `-o Dir=` to point at host filesystem
- Download + manual extraction to bypass shims entirely

**Network Policy Bypass**
- DNS exfiltration even under `NetworkDeny`
- Raw socket access under `root` profile
- IPv6 bypass if only IPv4 is filtered
- Unix domain sockets as covert channel
- Shell-level denial bypass via `curl --unix-socket`

**Resource Limit Bypass**
- Fork bomb within limits
- File descriptor exhaustion
- `/dev/shm` or `tmpfs` usage outside of tracked quota
- Output truncation to hide activity

**Shell Interpreter Quirks**
- `mvdan.cc/sh` vs `/bin/sh` behavioral differences — commands that parse differently
- Process substitution `<(cmd)` to spawn untracked subprocesses
- `eval` with crafted strings to bypass static analysis
- Heredoc tricks, `$'...'` ANSI-C quoting, `$()` nesting depth

---

## Phase 2 — Vulnerability Summary (USER INTERACTION REQUIRED)

Before executing anything, present a **concise summary** to the user in this format:

```
## Jailbreak Summary

**Attack surface identified:** [N] potential vulnerabilities across [M] isolation layers

**High confidence (likely to work):**
- [brief one-liner per finding, e.g. "Symlink escape: sandbox allows symlink creation, OpenHandler may follow links"]
- ...

**Medium confidence (worth trying):**
- [brief one-liner per finding]
- ...

**Low confidence / exploratory:**
- [brief one-liner per finding]
- ...

**Docker profiles to spin up:** [list which profiles and why]

Suggestions? Any attack vectors to add, remove, or prioritize?
Type 'go' to proceed with the plan as-is, or give feedback.
```

Wait for the user's response before proceeding.

- If the user says "go" or similar approval, proceed to Phase 3.
- If the user asks to "show plan", print the full detailed attack plan with all techniques and then ask again.
- If the user gives feedback, incorporate it into the plan, then confirm the updated summary and ask again.
- Do NOT proceed to execution without explicit user approval.

---

## Phase 3 — Environment Setup

Based on your attack plan and the user's input, decide which Docker profiles to spin up. Use the Makefile targets.

To see available targets: `make help` or read the Makefile directly.

Typical commands will be like:
- `make docker-root` — start root profile container
- `make docker-sudoer` — start sudoer profile container
- `make docker-locked` — start locked (hardened) profile container

Check what targets actually exist first by reading the Makefile. Use the actual target names.

Spin up the required profiles. If `--profile` was specified in $ARGUMENTS, respect that. Otherwise, spin up the profiles most relevant to your highest-confidence attacks.

For attacks that don't require Docker (e.g., code analysis, unit-level escape via Go test), note them separately and run them locally.

---

## Phase 4 — Adaptive Execution

Execute your attack plan. For each attempt:

1. **Run the attack** inside the appropriate container/environment
2. **Evaluate the result** — did it escape? partial success? failed?
3. **Adapt contextually**:
   - If successful: document the exact escape path, then try variations to understand the full scope
   - If partially successful: probe the boundary — what exactly was blocked? modify the technique
   - If failed: analyze the error. Is the block from Landlock? Namespace? Intercept? Choose the next most promising technique based on the failure mode
4. **Run parallel attacks** where they are independent — e.g., filesystem attacks in one container while network attacks run in another

**Tracking state**: maintain a mental ledger of:
- Confirmed escapes (with reproduction steps)
- Interesting partial results
- Definitively blocked (with block reason)

**Adaptive rules**:
- If a whole category repeatedly fails with the same root cause, document it and move on — don't exhaust attempts on a working defense
- If something partially works, double down and explore variations before moving on
- If an attack opens a new attack surface not in the original plan, pursue it

---

## Phase 5 — Report Generation

After all attacks are complete (or when the user signals to stop), generate a comprehensive report.

Create the file `/reports/jailbreak-<YYYY-MM-DD-HH-MM>.md` with this structure:

```markdown
# Sandbox Jailbreak Report
**Date:** <date>
**Target:** agentic-bash sandboxing library
**Profiles tested:** <list>
**Isolation layers tested:** <list>

---

## Executive Summary
[2-3 paragraph overview: what was tested, what was found, overall security posture]

---

## Confirmed Escapes

### [Escape Name]
**Severity:** Critical / High / Medium
**Isolation layer bypassed:** [Landlock / Namespace / FS / Intercept / Network / Package]
**Docker profile affected:** [root / sudoer / locked / all]

**Vulnerability:**
[Technical description of the bug]

**Reproduction steps:**
```bash
# exact commands that demonstrate the escape
```

**Impact:**
[What an attacker can do with this]

**Root cause in code:**
[File path:line reference to where the vulnerability exists]

**Suggested fix:**
[Concrete code-level recommendation]

---

## Partial Successes / Interesting Behaviors
[For each: description, what worked, what blocked it, why it's notable]

---

## Tested and Blocked (Defense Validation)
[For each: technique tried, how it was blocked, which mechanism stopped it — this validates the defenses work]

---

## Attack Surface Notes
[Observations about the codebase that should be addressed even if not directly exploitable]

---

## Recommendations
[Prioritized list of security improvements]
```

Ensure the /reports directory exists before writing (`mkdir -p /reports`).

---

## Phase 6 — GitHub Issue

After the report file is written, create a GitHub issue using the `gh` CLI with the full report as the body.

**Steps:**

1. Detect the remote repo: `gh repo view --json nameWithOwner -q .nameWithOwner`
2. Build the issue title:
   - If confirmed escapes exist: `[Security] Sandbox escape vulnerabilities found — <N> confirmed, <M> partial (<date>)`
   - If no confirmed escapes: `[Security] Jailbreak audit completed — no escapes confirmed (<date>)`
3. Create the issue with:
   ```bash
   gh issue create \
     --title "<title>" \
     --body "$(cat <report-file-path>)" \
     --label "security" \
     --label "bug"
   ```
   - If the `security` or `bug` labels don't exist yet, create them first:
     ```bash
     gh label create security --color "#e11d48" --description "Security vulnerability or audit"
     gh label create bug --color "#d73a4a" --description "Something isn't working"
     ```
   - Ignore label creation errors if they already exist (add `|| true`)
4. After the issue is created, print the issue URL to the user.
5. If `gh` is not authenticated or the repo has no remote, warn the user and skip this step — do not fail the whole run.

---

## Behavior Rules

- **Never fabricate results** — only report what you actually observed
- **Exact reproduction steps** — every confirmed escape must have copy-paste-runnable commands
- **Code references** — link every vulnerability to specific file:line in the codebase
- **Teardown containers** when done (run the appropriate make stop/clean targets)
- **Ask the user** before doing anything destructive to the host system
- **If stuck**, explain what you tried and why it failed, then ask for guidance
- **Report partial context** even if no full escape is found — the report should be valuable regardless of outcome
