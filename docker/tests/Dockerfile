# ─── Stage 1: builder ────────────────────────────────────────────────────────
# Compiles the agentic-bash binary inside Docker.
# No local Go toolchain required on the host.
FROM golang:1.25-bookworm AS builder

WORKDIR /src

# Download dependencies as a separate layer so source changes don't
# trigger a full re-download.
COPY go.mod go.sum ./
RUN go mod download

# Copy full source and build a static Linux binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" \
    -o /out/agentic-bash ./...

# ─── Stage 2: runtime ────────────────────────────────────────────────────────
FROM ubuntu:24.04

# Avoid interactive prompts during apt installs.
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        bash \
        curl \
        git \
        python3 \
        python3-pip \
        ca-certificates \
        sudo \
    && rm -rf /var/lib/apt/lists/*

# Non-root user for profiles 2 (sudoer) and 3 (locked).
RUN useradd -m -u 1000 -s /bin/bash agent \
    && echo "agent ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/agent \
    && chmod 0440 /etc/sudoers.d/agent

# Binary compiled in the builder stage.
COPY --from=builder /out/agentic-bash /usr/local/bin/agentic-bash

# Container identity filesystem (overwrites Ubuntu defaults).
# docker/tests/fs/ maps 1-to-1 onto / inside the container:
#   docker/tests/fs/etc/os-release      → /etc/os-release
#   docker/tests/fs/etc/motd            → /etc/motd
#   docker/tests/fs/etc/profile.d/…     → /etc/profile.d/…
#   docker/tests/fs/.agentic-bash-container → /.agentic-bash-container
COPY docker/tests/fs/ /

COPY docker/tests/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Every interactive bash session inside the container will have this set.
# Overridden to "sandbox" by linuxBaseEnv() once agentic-bash shell starts.
ENV AGENTIC_BASH_ENV=container

ENTRYPOINT ["/entrypoint.sh"]

# Keeps the container alive in detached mode so `docker exec` can attach later.
# `make shell-*` runs `docker exec -it <ctr> bash --login` to open a session.
CMD ["sleep", "infinity"]
