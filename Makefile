.DEFAULT_GOAL := web

VERSION ?= dev
UPDATE_UA ?= $(if $(filter dev,$(VERSION)),0,1)
BUILD_LDFLAGS := -X vista/internal/buildinfo.Version=$(VERSION)

TEST_GOALS := go frontend e2e integration mcp memory capacity
TEST_STATE := $(CURDIR)/.vista/test-state
TEST_BIN := $(TEST_STATE)/bin/vista
TEST_E2E_BIN := $(TEST_STATE)/bin/vista-e2e
TEST_MCP_FIXTURE := $(TEST_STATE)/bin/mcp-fixture
TEST_ENV = VISTA_BIN="$(TEST_BIN)" VISTA_MCP_FIXTURE="$(TEST_MCP_FIXTURE)" VISTA_TEST_TMPDIR="$(TEST_STATE)"
VISTA_CHROME ?= $(shell command -v google-chrome || command -v google-chrome-stable || command -v chromium || command -v chromium-browser)
HARBOR_PYTHON ?= $(CURDIR)/.vista/benchmarks/harbor/venv/bin/python
BENCHMARK_ARGS ?= --help

INTEGRATION_SCRIPTS := artifact background_job catalog checklist copilot_startup \
	escaped_input execution_efficiency gemini history iteration_flow long_context \
	mistral security session_conflict sqlite_fault tool_continuation worker_crash
IMAGE_PROTOCOLS := openai-chat mistral cohere bedrock vercel-gateway \
	bedrock-mantle-responses sap-ai-core copilot

.PHONY: build update-ua licenses check-licenses package test web benchmark $(TEST_GOALS) all test-bin test-mcp-fixture \
	test-frontend-unit test-frontend-types check-browser check-sqlite
# Memory measurements and builds share resources, including with multiple goals.
.NOTPARALLEL:

ifneq ($(filter test,$(MAKECMDGOALS)),)
ifneq ($(filter-out test $(TEST_GOALS) all,$(MAKECMDGOALS)),)
$(error Unknown test goal(s): $(filter-out test $(TEST_GOALS) all,$(MAKECMDGOALS)). Use: make test [$(TEST_GOALS) all])
endif
endif

web/node_modules/.package-lock.json: web/package.json web/package-lock.json
	npm --prefix web ci

scripts/mcp_upstream/node_modules/.package-lock.json: scripts/mcp_upstream/package.json scripts/mcp_upstream/package-lock.json
	npm --prefix scripts/mcp_upstream ci --ignore-scripts

web: web/node_modules/.package-lock.json
	npm --prefix web run build

update-ua:
	node scripts/update_user_agents.mjs

licenses: web
	node scripts/update_licenses.mjs

check-licenses: web
	node scripts/update_licenses.mjs --check

build: $(if $(filter 1,$(UPDATE_UA)),update-ua) licenses
	mkdir -p bin
	GOWORK=off CGO_ENABLED=0 go build -ldflags="$(BUILD_LDFLAGS)" -o bin/vista ./cmd/vista

package: build
	python3 scripts/package_release.py --binary bin/vista --version "$(VERSION)" --output-dir .vista/dist

benchmark:
	"$(HARBOR_PYTHON)" benchmarks/harbor/run.py $(BENCHMARK_ARGS)

test: $(if $(filter-out test,$(MAKECMDGOALS)),,go)
	@:

go:
	GOWORK=off CGO_ENABLED=0 go test ./...
	GOWORK=off CGO_ENABLED=0 go vet ./...
	GOWORK=off CGO_ENABLED=1 go test -race ./...

test-frontend-unit: web/node_modules/.package-lock.json
	npm --prefix web run test:unit
	node --test scripts/update_user_agents.test.mjs
	node --test scripts/update_licenses.test.mjs

test-frontend-types: web/node_modules/.package-lock.json
	npm --prefix web run typecheck

frontend: test-frontend-unit test-frontend-types web

check-browser:
	@test -n "$(VISTA_CHROME)" && test -x "$(VISTA_CHROME)" || { printf '%s\n' 'Set VISTA_CHROME to a Chrome/Chromium executable before running e2e or all.' >&2; exit 1; }

check-sqlite:
	@command -v sqlite3 >/dev/null || { printf '%s\n' 'Install sqlite3 before running integration or all.' >&2; exit 1; }

test-bin:
	mkdir -p "$(TEST_STATE)/bin"
	GOWORK=off CGO_ENABLED=0 go build -ldflags="$(BUILD_LDFLAGS)" -o "$(TEST_BIN)" ./cmd/vista

test-mcp-fixture:
	mkdir -p "$(TEST_STATE)/bin"
	GOWORK=off CGO_ENABLED=0 go build -o "$(TEST_MCP_FIXTURE)" ./scripts/mcp_fixture

e2e: check-browser web
	mkdir -p "$(TEST_STATE)/bin"
	GOWORK=off CGO_ENABLED=0 go build -ldflags="$(BUILD_LDFLAGS)" -o "$(TEST_E2E_BIN)" ./cmd/vista
	VISTA_BIN="$(TEST_E2E_BIN)" VISTA_CHROME="$(VISTA_CHROME)" npm --prefix web run test:e2e -- --max-failures=1

integration: check-sqlite test-bin
	VISTA_TEST_BINARY="$(TEST_BIN)" GOWORK=off CGO_ENABLED=0 go test ./cmd/vista -run '^TestNative(Application|ResetPort|WindowsDesktopLauncher|WindowsTray.*)$$' -count=1
	@set -eu; \
	for script in $(INTEGRATION_SCRIPTS); do \
		printf '\nRunning scripts/%s_smoke.mjs\n' "$$script"; \
		$(TEST_ENV) VISTA_CRASH_MASTER=0 VISTA_DOUBLE_CRASH=0 node "scripts/$${script}_smoke.mjs"; \
	done
	$(TEST_ENV) VISTA_CRASH_MASTER=1 node scripts/worker_crash_smoke.mjs
	$(TEST_ENV) VISTA_DOUBLE_CRASH=1 node scripts/background_job_smoke.mjs

mcp: test-bin test-mcp-fixture scripts/mcp_upstream/node_modules/.package-lock.json
	$(TEST_ENV) node scripts/mcp_e2e_smoke.mjs
	$(TEST_ENV) node scripts/mcp_upstream_smoke.mjs

memory: test-bin
	$(TEST_ENV) VISTA_EXTRA_TURNS=200 VISTA_MEMORY_REGRESSION=1 node scripts/capacity_smoke.mjs

capacity: test-bin test-mcp-fixture
	@set -eu; \
	for transport in stdio http; do \
		printf '\nRunning mixed capacity (%s)\n' "$$transport"; \
		$(TEST_ENV) VISTA_MCP_TRANSPORT="$$transport" node scripts/mixed_capacity_smoke.mjs; \
	done
	$(TEST_ENV) VISTA_LONG_OUTPUT_RESPONSES=0 node scripts/long_output_smoke.mjs
	$(TEST_ENV) VISTA_LONG_OUTPUT_RESPONSES=1 node scripts/long_output_smoke.mjs
	$(TEST_ENV) node scripts/slow_sse_capacity_smoke.mjs
	@set -eu; \
	for protocol in $(IMAGE_PROTOCOLS); do \
		printf '\nRunning image capacity (%s)\n' "$$protocol"; \
		$(TEST_ENV) VISTA_IMAGE_PROTOCOL="$$protocol" node scripts/image_capacity_smoke.mjs; \
	done

all: check-browser check-sqlite $(TEST_GOALS)
