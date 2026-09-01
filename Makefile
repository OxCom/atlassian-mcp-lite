# Every target runs in a pinned container. Nothing needs to be installed on the
# host except Docker. See compose.dev.yaml for the tool versions.

# compose.yaml appears at Task 14; compose.override.yaml is the machine-local
# corporate-network file and is gitignored. Both are optional, so each is
# included only when present.
#
# Compose auto-loads an override ONLY for the default compose.yaml. Passing -f
# disables that, so the override is added explicitly here — otherwise the office
# proxy and CA settings would silently not apply.
COMPOSE_SRC := $(wildcard compose.yaml) compose.dev.yaml $(wildcard compose.override.yaml)
COMPOSE_FILES := $(foreach f,$(COMPOSE_SRC),-f $(f))

DC := docker compose $(COMPOSE_FILES) run --rm --quiet-pull

.DEFAULT_GOAL := help

.PHONY: help
help: ## show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

## --- the two you will use most ---

.PHONY: check
check: security lint test ## everything CI runs on a branch

.PHONY: test
test: ## run tests with the race detector and coverage
	$(DC) test

## --- individual stages ---

# Runs every scan even when one fails, then fails at the end. A security sweep
# that stops at the first finding hides the rest, and make's default behaviour
# would have skipped gosec entirely.
.PHONY: security
security: ## all security scans; runs them all, then fails if any did
	@rc=0; \
	for t in gitleaks trufflehog govulncheck gosec; do \
		printf '\n=== %s ===\n' "$$t"; \
		$(MAKE) --no-print-directory $$t || rc=1; \
	done; \
	if [ $$rc -ne 0 ]; then echo; echo "security: at least one scan failed"; fi; \
	exit $$rc

.PHONY: gitleaks
gitleaks: ## scan working tree and git history for secrets
	$(DC) gitleaks

.PHONY: trufflehog
trufflehog: ## scan filesystem for verified secrets
	$(DC) trufflehog

.PHONY: govulncheck
govulncheck: ## dependency vulnerabilities reachable from this code
	$(DC) govulncheck

.PHONY: gosec
gosec: ## static security analysis of our own code
	$(DC) gosec

.PHONY: lint
lint: ## golangci-lint
	$(DC) lint

.PHONY: fmt
fmt: ## apply gofmt and goimports
	$(DC) fmt

.PHONY: vet
vet: ## go vet
	$(DC) vet

.PHONY: tidy
tidy: ## fail if go.mod or go.sum would change
	$(DC) tidy

.PHONY: cover
cover: ## per-function coverage report
	$(DC) cover

.PHONY: build
build: ## build the binary into ./bin
	$(DC) build

.PHONY: image
image: ## build the production container image
	DOCKER_BUILDKIT=1 docker compose $(COMPOSE_FILES) build atlassian-mcp-lite

.PHONY: clean
clean: ## remove build output and caches
	rm -rf bin coverage.out
	docker compose $(COMPOSE_FILES) down -v
