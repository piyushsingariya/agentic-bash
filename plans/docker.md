# Plan: Docker Containerisation

## Problem

Running `agentic-bash` bare on the host machine is risky. The process isolation the
sandbox provides (namespaces, landlock, cgroups) operates inside the host OS. One
misconfig, one unguarded `exec`, and the host filesystem is exposed.

We need a Docker layer that:
1. Puts a hard OS boundary around everything before agentic-bash even starts.
2. Provides three escalating privilege profiles for different developer needs.
3. Makes it immediately clear — at every shell prompt — which "layer" you are in:
   host machine, Docker container, or agentic-bash virtual sandbox.

---

## The Three Layers (and how to tell them apart)

```
┌────────────────────────────────────────────────────────┐
│  Layer 0 — Host machine (macOS / developer workstation)│
│  /etc/os-release ID=macos  (or ubuntu, arch, etc.)     │
│  hostname: your-macbook.local                          │
└────────────────────────────┬───────────────────────────┘
                             │  docker run
┌────────────────────────────▼───────────────────────────┐
│  Layer 1 — Docker container  ← THIS IS THE NEW LAYER   │
│  /etc/os-release ID=agentic-bash-vm                    │
│  hostname: agentic-bash-vm                             │
│  /.agentic-bash-container  (marker file)               │
│  AGENTIC_BASH_ENV=container                            │
│  PS1 prefix: [container] ...                           │
└────────────────────────────┬───────────────────────────┘
                             │  ./agentic-bash shell
┌────────────────────────────▼───────────────────────────┐
│  Layer 2 — agentic-bash virtual sandbox (already impl) │
│  /etc/os-release ID=agentic-bash                       │
│  hostname: sandbox                                     │
│  AGENTIC_BASH_ENV=sandbox                              │
│  PS1 prefix: [sandbox] user@sandbox:~$                 │
└────────────────────────────────────────────────────────┘
```

Any script or agent can check `$AGENTIC_BASH_ENV`:
- empty / unset    → running on the raw host
- `container`      → inside the Docker container but not in a sandbox
- `sandbox`        → inside the agentic-bash virtual shell

Any human can see it immediately in the PS1 prefix.

---

## Security Profiles

### Profile 1 — `root`
Container runs as `root`. All Linux capabilities present. Full sudo.
Use when: you need to test Linux namespace features (`CLONE_NEWUSER`, cgroup
mount, etc.) that require real root inside the container.

```
User inside:     root (uid=0)
sudo:            N/A — already root
cap_drop:        none
no-new-privs:    false
seccomp:         default Docker profile
```

### Profile 2 — `sudoer`
Container starts as non-root user `agent` (uid=1000). `sudo` binary present and
passwordless. Agent can `sudo su`, `sudo bash`, install packages, etc.
Use when: you want a realistic non-root default but need an escape hatch for
debugging or package installation.

```
User inside:     agent (uid=1000)
sudo:            installed; NOPASSWD for agent
cap_drop:        NET_RAW, SYS_PTRACE
no-new-privs:    false
seccomp:         default Docker profile
```

### Profile 3 — `locked`
Container starts as `agent` (uid=1000). `sudo` is NOT installed. Capabilities
are dropped to a minimal set. `--security-opt no-new-privileges` prevents any
setuid binary from gaining elevated permissions.
Use when: running agentic-bash in a production or untrusted-code scenario where
the container must not be escapable.

```
User inside:     agent (uid=1000)
sudo:            not installed
cap_drop:        ALL
cap_add:         CHOWN, DAC_OVERRIDE, FOWNER, SETUID, SETGID  (minimum for apt/go)
no-new-privs:    true
seccomp:         default Docker profile
read-only root:  optional (see future work)
```

---

## File Layout

```
agentic-bash/
├── docker/
│   └── tests/                  # dev / pentesting environment
│       ├── Dockerfile          # multi-stage: golang builder → ubuntu runtime
│       ├── .dockerignore       # excludes .git/, plans/, local binaries from build context
│       ├── entrypoint.sh       # prints MOTD + context, then execs CMD
│       └── fs/                 # files COPY'd verbatim into the image root
│           ├── etc/
│           │   ├── os-release  # ID=agentic-bash-vm  (overwrites Ubuntu default)
│           │   ├── motd        # welcome banner
│           │   └── profile.d/
│           │       └── agentic-bash.sh   # sets PS1 prefix + AGENTIC_BASH_ENV=container
│           └── .agentic-bash-container   # marker file placed at /
└── Makefile
```

---

## Dockerfile Design

**No local Go toolchain required.** The developer only needs Docker.
`make build` is the single entry point — Go compilation happens entirely
inside Docker.

### Multi-stage build

```
┌─────────────────────────────────────────────────────────┐
│  Stage 1 — builder  (golang:1.25-bookworm)              │
│                                                         │
│  WORKDIR /src                                           │
│                                                         │
│  # Layer 1: dependency cache                            │
│  COPY go.mod go.sum ./                                  │
│  RUN go mod download                                    │
│                                                         │
│  # Layer 2: source                                      │
│  COPY . .                                               │
│                                                         │
│  # Compile — static Linux binary, no CGO               │
│  RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \            │
│      go build -ldflags="-s -w" \                        │
│      -o /out/agentic-bash ./...                         │
└─────────────────────────────────────────────────────────┘
                          │  COPY --from=builder
┌─────────────────────────▼───────────────────────────────┐
│  Stage 2 — runtime  (ubuntu:24.04)                      │
│                                                         │
│  # System packages needed at runtime                    │
│  RUN apt-get install -y \                               │
│        bash curl git python3 python3-pip \              │
│        ca-certificates sudo                             │
│                                                         │
│  # Non-root user for profiles 2+3                       │
│  RUN useradd -m -u 1000 -s /bin/bash agent && \         │
│      echo "agent ALL=(ALL) NOPASSWD:ALL" \              │
│        > /etc/sudoers.d/agent                           │
│                                                         │
│  # Binary from builder stage                            │
│  COPY --from=builder /out/agentic-bash \                │
│       /usr/local/bin/agentic-bash                       │
│                                                         │
│  # Container identity filesystem                        │
│  COPY docker/tests/fs/ /                                │
│  COPY docker/tests/entrypoint.sh /entrypoint.sh         │
│  RUN chmod +x /entrypoint.sh                            │
│                                                         │
│  ENV AGENTIC_BASH_ENV=container                         │
│  ENTRYPOINT ["/entrypoint.sh"]                          │
│  CMD ["bash"]                                           │
└─────────────────────────────────────────────────────────┘
```

### Why `CGO_ENABLED=0`

The binary is compiled with CGO disabled, producing a fully static executable
with no shared library dependencies. This means:
- The runtime image does not need matching `glibc` versions.
- The binary runs identically regardless of which Ubuntu packages are installed.
- No `libc.so` / `libpthread.so` missing errors in the container.

The `-ldflags="-s -w"` strips debug info and the symbol table, reducing binary
size significantly.

### Why module cache is a separate layer

```dockerfile
COPY go.mod go.sum ./
RUN go mod download          ← cached unless go.mod/go.sum change
COPY . .
RUN go build ...             ← only re-runs when source changes
```

Downloading modules is slow (~30s on first run). Keeping it as a separate
layer means rebuilds after source-only changes skip the download entirely.

### `.dockerignore`

Must be added alongside the Dockerfile to keep the build context small
(excludes `.git/`, test artifacts, local binaries):

```
.git
*.test
/agentic-bash          # any locally compiled binary
/tmp
/plans
```

### Single image, three runtime profiles

The same `agentic-bash:local` image is used for all three profiles.
Profile differences are expressed entirely as `docker run` flags — no
separate images, no separate build steps.

### Build arg: INSTALL_SUDO

```dockerfile
ARG INSTALL_SUDO=true
RUN if [ "$INSTALL_SUDO" = "true" ]; then \
      apt-get install -y sudo && \
      echo "agent ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers.d/agent; \
    fi
```

By default sudo is installed (needed for profiles 1+2). The `locked` profile
does not need to rebuild — it just never calls sudo and the container's
`--security-opt no-new-privileges` makes the binary non-functional even if
somehow present.

---

## Container Filesystem Bootstrap (Layer 1 identity)

These files are baked into the image at build time. They establish the
"container" identity that distinguishes Layer 1 from both the host and the
agentic-bash virtual sandbox.

### `/etc/os-release` (overwrite Ubuntu's default)

```ini
PRETTY_NAME="agentic-bash VM (container)"
NAME="agentic-bash-vm"
ID=agentic-bash-vm
ID_LIKE=ubuntu
VERSION="1.0"
HOME_URL="https://github.com/piyushsingariya/agentic-bash"
AGENTIC_BASH_LAYER="container"
```

Key field: `ID=agentic-bash-vm` — distinct from the sandbox's `ID=agentic-bash`.

### `/.agentic-bash-container`

An empty marker file. Scripts can test `[ -f /.agentic-bash-container ]` to
detect container context without parsing text files.

```
# created at /
```

### `/etc/motd`

Displayed on every interactive login (via `entrypoint.sh`):

```
╔═══════════════════════════════════════════════╗
║         agentic-bash container               ║
║                                               ║
║  Layer : container (Docker)                   ║
║  User  : <resolved at runtime>                ║
║  Run   : agentic-bash shell   ← to enter      ║
║          the virtual sandbox                  ║
║                                               ║
║  AGENTIC_BASH_ENV=container                   ║
╚═══════════════════════════════════════════════╝
```

### `/etc/profile.d/agentic-bash.sh`

Sourced by every interactive bash session inside the container:

```bash
export AGENTIC_BASH_ENV=container

# PS1 prefix: red tag so it's visually unmistakable
export PS1='\[\e[0;31m\][container]\[\e[0m\] \u@\h:\w\$ '
```

Note: The agentic-bash sandbox ALREADY sets its own env and PS1 via its
`bootstrapFS`. So once you're inside the sandbox, the container PS1 is
completely overridden. The two layers are visually distinct without any
extra coordination.

### `/etc/hostname` (set at runtime via `--hostname`)

Not baked in. Set via `docker run --hostname agentic-bash-vm` so it shows
in the shell prompt and in `hostname` output.

---

## entrypoint.sh

```bash
#!/usr/bin/env bash
set -e

# Print MOTD
cat /etc/motd
echo "  Hostname : $(hostname)"
echo "  User     : $(id -un) (uid=$(id -u))"
echo ""

exec "$@"
```

Simple. Prints context, then hands off to CMD (default: `bash`).

---

## Makefile

All targets are documented with `make help`.

### Variables (at top of Makefile)

```makefile
IMAGE_NAME    := agentic-bash
IMAGE_TAG     := local
IMAGE         := $(IMAGE_NAME):$(IMAGE_TAG)

CTR_ROOT      := agentic-bash-root
CTR_SUDOER    := agentic-bash-sudoer
CTR_LOCKED    := agentic-bash-locked

HOSTNAME      := agentic-bash-vm
```

### Targets

```
make help              Print all targets with descriptions

make build             Build the Docker image (agentic-bash:local)
make rebuild           Force rebuild (no cache)

make up-root           Create + start the root container (profile 1)
make up-sudoer         Create + start the sudoer container (profile 2)
make up-locked         Create + start the locked container (profile 3)

make shell-root        Exec an interactive bash into the root container
make shell-sudoer      Exec an interactive bash into the sudoer container
make shell-locked      Exec an interactive bash into the locked container

make down-root         Stop + remove the root container
make down-sudoer       Stop + remove the sudoer container
make down-locked       Stop + remove the locked container
make down              Stop + remove ALL three containers

make status            Show running status of all three containers
make logs-root         Tail logs from root container
make logs-sudoer       Tail logs from sudoer container
make logs-locked       Tail logs from locked container

make clean             down + remove image
make clean-image       Remove the image only (leave containers)
```

### `make up-*` implementation detail

Each `up-*` target uses `docker run` (not compose) to keep the setup
self-contained. Containers are started detached (`-d`) and kept alive by
running `sleep infinity` as the CMD so that `make shell-*` can exec in.

If a container with the same name already exists, the target stops and
removes it first (`docker rm -f`) before recreating, making `make up-*`
idempotent.

#### `up-root` flags
```
docker run -d \
  --name $(CTR_ROOT) \
  --hostname $(HOSTNAME) \
  -e AGENTIC_BASH_PROFILE=root \
  --privileged \
  $(IMAGE) sleep infinity
```

`--privileged` gives full Linux capabilities needed to test namespace
isolation inside the container.

#### `up-sudoer` flags
```
docker run -d \
  --name $(CTR_SUDOER) \
  --hostname $(HOSTNAME) \
  --user agent \
  -e AGENTIC_BASH_PROFILE=sudoer \
  --cap-drop NET_RAW \
  --cap-drop SYS_PTRACE \
  $(IMAGE) sleep infinity
```

#### `up-locked` flags
```
docker run -d \
  --name $(CTR_LOCKED) \
  --hostname $(HOSTNAME) \
  --user agent \
  -e AGENTIC_BASH_PROFILE=locked \
  --cap-drop ALL \
  --cap-add  CHOWN \
  --cap-add  DAC_OVERRIDE \
  --cap-add  FOWNER \
  --cap-add  SETUID \
  --cap-add  SETGID \
  --security-opt no-new-privileges \
  $(IMAGE) sleep infinity
```

#### `shell-*` flags
```
docker exec -it $(CTR_ROOT) bash --login
```
`--login` sources `/etc/profile.d/agentic-bash.sh` so the PS1 and
`AGENTIC_BASH_ENV` are always set correctly on entry.

---

## Layer Recognition — Complete Matrix

| Check                         | Host           | Container              | Sandbox              |
|-------------------------------|----------------|------------------------|----------------------|
| `$AGENTIC_BASH_ENV`           | unset          | `container`            | `sandbox`            |
| `/etc/os-release ID=`         | host OS        | `agentic-bash-vm`      | `agentic-bash`       |
| `hostname`                    | your machine   | `agentic-bash-vm`      | `sandbox`            |
| `[ -f /.agentic-bash-container ]` | false      | true                   | false (virtual FS)   |
| `$USER`                       | your user      | `root` or `agent`      | `user`               |
| `$PS1` prefix                 | none           | `[container]`          | (sandbox default)    |
| `cat /etc/motd`               | nothing        | agentic-bash banner    | not set              |

The sandbox already writes `ID=agentic-bash` in its `/etc/os-release` via
`bootstrapFS`. The `AGENTIC_BASH_ENV=sandbox` variable needs to be added to
the `linuxBaseEnv()` function in `sandbox/session.go` so every sandbox
inherits it automatically regardless of how it's launched.

---

## agentic-bash Source Change: `AGENTIC_BASH_ENV=sandbox`

One small addition required to make the matrix complete.

**File:** `sandbox/session.go` — `linuxBaseEnv()`

Add one line:

```go
"AGENTIC_BASH_ENV": "sandbox",
```

This ensures that once an agent runs `agentic-bash shell` inside the
container, the `$AGENTIC_BASH_ENV` variable flips from `container` to
`sandbox` automatically.

---

## Implementation Order

```
1. docker/fs/ skeleton
   ├── etc/os-release        (container identity)
   ├── etc/motd              (welcome banner)
   ├── etc/profile.d/        (PS1 + AGENTIC_BASH_ENV=container)
   └── .agentic-bash-container  (marker file)

2. docker/entrypoint.sh      (print MOTD, exec CMD)

3. docker/Dockerfile         (multi-stage: builder + runtime)

4. Makefile                  (build + up/down/shell targets for all 3 profiles)

5. sandbox/session.go        (add AGENTIC_BASH_ENV=sandbox to linuxBaseEnv)
```

Steps 1–4 are purely Docker/infra. Step 5 is the only Go source change.

---

## Non-Goals

- Docker Compose: overkill for a single-container dev tool; plain `docker run`
  in Makefile is simpler and has zero extra dependencies.
- Separate Dockerfiles per profile: profiles differ only in runtime flags, not
  image content. One image, three run invocations.
- Volume mounts: not included by default. Developer can add `-v` to the
  `docker run` commands manually if they want to share files.
- Networking: default Docker bridge network is used (not `--network none`)
  because agentic-bash itself controls network isolation via its `NetworkPolicy`.
  The container layer does not double-restrict networking.
- Port exposure: no HTTP server; nothing to expose.
- Multi-arch / CI push: out of scope. Image is local-only (`agentic-bash:local`).

---

## Files to Create / Modify

| File                                      | Action   |
|-------------------------------------------|----------|
| `docker/tests/Dockerfile`                       | new      |
| `.dockerignore`                                 | new      |
| `docker/tests/entrypoint.sh`                    | new      |
| `docker/tests/fs/etc/os-release`                | new      |
| `docker/tests/fs/etc/motd`                      | new      |
| `docker/tests/fs/etc/profile.d/agentic-bash.sh` | new      |
| `docker/tests/fs/.agentic-bash-container`       | new      |
| `Makefile`                                | new      |
| `sandbox/session.go`                      | modify   |
