IMAGE_NAME   := agentic-bash
IMAGE_TAG    := local
IMAGE        := $(IMAGE_NAME):$(IMAGE_TAG)
DOCKERFILE   := docker/tests/Dockerfile

CTR_ROOT     := agentic-bash-root
CTR_SUDOER   := agentic-bash-sudoer
CTR_LOCKED   := agentic-bash-locked

HOSTNAME     := agentic-bash-vm

.PHONY: help build rebuild \
        up-root up-sudoer up-locked \
        shell-root shell-sudoer shell-locked \
        down-root down-sudoer down-locked down \
        status \
        logs-root logs-sudoer logs-locked \
        clean clean-image

# ─── help ─────────────────────────────────────────────────────────────────────

help:
	@echo ""
	@echo "  agentic-bash · docker/tests targets"
	@echo ""
	@echo "  Build"
	@echo "    make build            Build image $(IMAGE) (binary compiled inside Docker)"
	@echo "    make rebuild          Force rebuild with --no-cache"
	@echo ""
	@echo "  Profiles"
	@echo "    make up-root          Start container: root user, --privileged (profile 1)"
	@echo "    make up-sudoer        Start container: agent user, passwordless sudo (profile 2)"
	@echo "    make up-locked        Start container: agent user, no sudo, caps dropped (profile 3)"
	@echo ""
	@echo "  Shell"
	@echo "    make shell-root       Exec bash into root container   (starts it if needed)"
	@echo "    make shell-sudoer     Exec bash into sudoer container (starts it if needed)"
	@echo "    make shell-locked     Exec bash into locked container (starts it if needed)"
	@echo ""
	@echo "  Teardown"
	@echo "    make down-root        Stop + remove root container"
	@echo "    make down-sudoer      Stop + remove sudoer container"
	@echo "    make down-locked      Stop + remove locked container"
	@echo "    make down             Stop + remove ALL three containers"
	@echo ""
	@echo "  Observe"
	@echo "    make status           Show running status of all containers"
	@echo "    make logs-root        Tail logs from root container"
	@echo "    make logs-sudoer      Tail logs from sudoer container"
	@echo "    make logs-locked      Tail logs from locked container"
	@echo ""
	@echo "  Cleanup"
	@echo "    make clean            down + remove image"
	@echo "    make clean-image      Remove image only (leave containers)"
	@echo ""

# ─── build ────────────────────────────────────────────────────────────────────

build:
	docker build \
		-f $(DOCKERFILE) \
		-t $(IMAGE) \
		.

rebuild:
	docker build \
		--no-cache \
		-f $(DOCKERFILE) \
		-t $(IMAGE) \
		.

# ─── up ───────────────────────────────────────────────────────────────────────
# Each target removes any existing container with the same name first so that
# re-running is always idempotent. Containers run `sleep infinity` so that
# `make shell-*` can exec in at any time.

up-root:
	@docker rm -f $(CTR_ROOT) 2>/dev/null || true
	docker run -d \
		--name $(CTR_ROOT) \
		--hostname $(HOSTNAME) \
		-e AGENTIC_BASH_PROFILE=root \
		--privileged \
		$(IMAGE) sleep infinity

up-sudoer:
	@docker rm -f $(CTR_SUDOER) 2>/dev/null || true
	docker run -d \
		--name $(CTR_SUDOER) \
		--hostname $(HOSTNAME) \
		--user agent \
		-e AGENTIC_BASH_PROFILE=sudoer \
		--cap-drop NET_RAW \
		--cap-drop SYS_PTRACE \
		$(IMAGE) sleep infinity

up-locked:
	@docker rm -f $(CTR_LOCKED) 2>/dev/null || true
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

# ─── shell ────────────────────────────────────────────────────────────────────
# Each shell target ensures the container is running before exec-ing in.
# If the container doesn't exist yet, `up-*` creates it first.
# If it exists but is stopped, `docker start` wakes it.

shell-root:
	@docker inspect $(CTR_ROOT) > /dev/null 2>&1 || $(MAKE) --no-print-directory up-root
	@docker start  $(CTR_ROOT) > /dev/null 2>&1 || true
	docker exec -it $(CTR_ROOT) bash --login

shell-sudoer:
	@docker inspect $(CTR_SUDOER) > /dev/null 2>&1 || $(MAKE) --no-print-directory up-sudoer
	@docker start  $(CTR_SUDOER) > /dev/null 2>&1 || true
	docker exec -it $(CTR_SUDOER) bash --login

shell-locked:
	@docker inspect $(CTR_LOCKED) > /dev/null 2>&1 || $(MAKE) --no-print-directory up-locked
	@docker start  $(CTR_LOCKED) > /dev/null 2>&1 || true
	docker exec -it $(CTR_LOCKED) bash --login

# ─── down ─────────────────────────────────────────────────────────────────────

down-root:
	docker rm -f $(CTR_ROOT) 2>/dev/null || true

down-sudoer:
	docker rm -f $(CTR_SUDOER) 2>/dev/null || true

down-locked:
	docker rm -f $(CTR_LOCKED) 2>/dev/null || true

down: down-root down-sudoer down-locked

# ─── observe ──────────────────────────────────────────────────────────────────

status:
	@echo ""
	@printf "  %-30s %s\n" "CONTAINER" "STATUS"
	@printf "  %-30s %s\n" "---------" "------"
	@for ctr in $(CTR_ROOT) $(CTR_SUDOER) $(CTR_LOCKED); do \
		status=$$(docker inspect --format='{{.State.Status}}' $$ctr 2>/dev/null || echo "not created"); \
		printf "  %-30s %s\n" "$$ctr" "$$status"; \
	done
	@echo ""

logs-root:
	docker logs -f $(CTR_ROOT)

logs-sudoer:
	docker logs -f $(CTR_SUDOER)

logs-locked:
	docker logs -f $(CTR_LOCKED)

# ─── clean ────────────────────────────────────────────────────────────────────

clean-image:
	docker rmi $(IMAGE) 2>/dev/null || true

clean: down clean-image
