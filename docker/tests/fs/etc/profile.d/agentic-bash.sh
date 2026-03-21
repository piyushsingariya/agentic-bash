export AGENTIC_BASH_ENV=container

# Red [container] prefix — visually distinct from both the host and the sandbox.
# Once inside `agentic-bash shell`, bootstrapFS overwrites PS1 entirely.
export PS1='\[\e[0;31m\][container]\[\e[0m\] \u@\h:\w\$ '

# Print MOTD on every interactive login shell (covers both `docker run` and
# `docker exec ... bash --login` paths, since neither reliably prints it otherwise).
if [ -f /etc/motd ]; then
    cat /etc/motd
    printf "  Profile  : %s\n" "${AGENTIC_BASH_PROFILE:-unknown}"
    printf "  Hostname : %s\n" "$(hostname)"
    printf "  User     : %s (uid=%s)\n\n" "$(id -un)" "$(id -u)"
fi
