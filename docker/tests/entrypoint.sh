#!/usr/bin/env bash
set -e

# Minimal entrypoint — just hands off to CMD.
# MOTD and context info are printed by /etc/profile.d/agentic-bash.sh for all
# interactive login shells (both `docker run -it ... bash --login` and
# `docker exec -it ... bash --login`).
exec "$@"
