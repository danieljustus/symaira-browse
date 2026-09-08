SHELL := /bin/sh

BINARY := symbrowse
VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo 0.1.1)
GO ?= go
CARGO ?= cargo
# Match the toolchain CI formats with, so gofmt output cannot differ by host.
GO_VERSION := $(shell awk '$$1 == "go" { print $$2; exit }' go.mod)
CGO_ENABLED ?= 0
GOFLAGS ?=
LDFLAGS ?= -s -w -X main.version=$(VERSION)

.PHONY: build test test-race lint fmt-check clean port-oracle-build port-fixture-source-check port-core-fixtures-generate port-core-fixtures-check port-config-fixtures-generate port-config-fixtures-check port-policy-fixtures-generate port-policy-fixtures-check port-state-fixtures-generate port-state-fixtures-check port-mcp-fixtures-generate port-mcp-fixtures-check port-engine-fixtures-generate port-engine-fixtures-check port-injection-fixtures-generate port-injection-fixtures-check port-fetch-static-fixtures-generate port-fetch-static-fixtures-check port-fetch-control-fixtures-generate port-fetch-control-fixtures-check port-session-fixture-generate port-session-fixture-check port-workflow-fixture-generate port-workflow-fixture-check port-daemon-fixture-generate port-daemon-fixture-check differential-go-selftest port-benchmark port-value-signal port-contract rust-build rust-check rust-lint rust-test rust-features rust-security rust-version-contract rust-core-contract rust-policy-contract rust-state-contract rust-mcp-contract rust-engine-contract rust-injection-contract rust-fetch-static-slice rust-fetch-contract rust-session-contract rust-workflow-slice rust-safari-slice rust-browser-contract rust-native-browser-contract rust-daemon-contract rust-cdp-spike rust-miri rust-fuzz-smoke rust-inventory rust-hardening rust-release-gates rust-gates

PORT_ORACLE_COMMIT := 652453d1595fc302bd69c328e7da8a21dbee28b9
PORT_ORACLE_RELEASE := v0.8.0
PORT_GO_BINARY := target/port/symbrowse-go
PORT_ORACLE_WORKTREE := .worktrees/rust-oracle
PORT_CASES := testdata/port/bootstrap/cases.json
PORT_CORE_FIXTURE := testdata/port/core/output-budget-contract.json
PORT_CONFIG_FIXTURE := testdata/port/core/config-contract.json
PORT_POLICY_FIXTURE := testdata/port/policy/policy-contract.json
PORT_STATE_FIXTURE_DIR := testdata/port/state
PORT_MCP_FIXTURE_DIR := testdata/port/mcp
PORT_INJECTION_FIXTURE := testdata/port/injection/injection-contract.json
PORT_INJECTION_SOURCES := internal/injection/scan.go,internal/injection/boundary.go,internal/injection/patterns.txt
PORT_DAEMON_FIXTURE := testdata/port/daemon/protocol.json
PORT_DAEMON_SOURCES := internal/daemon/protocol.go,internal/daemon/client.go,internal/daemon/server.go
PORT_FETCH_STATIC_FIXTURE := port/fixtures/fetch/static.json
PORT_FETCH_STATIC_SOURCES := internal/fetch/agentdom/builder.go,internal/fetch/agentdom/document.go,internal/fetch/dom/extract.go,internal/fetch/dom/filter.go,internal/fetch/semantic/classify.go,internal/fetch/semantic/islands.go,internal/fetch/semantic/score.go,internal/fetch/render/frontmatter.go,internal/fetch/render/images.go,internal/fetch/render/json.go,internal/fetch/render/markdown.go,internal/fetch/render/schema.go,internal/fetch/render/text.go,internal/fetch/relevance/relevance.go,internal/fetch/pipeline/testdata
PORT_FETCH_CONTROL_FIXTURE := port/fixtures/fetch/control.json
PORT_FETCH_CONTROL_SOURCES := internal/fetch/fetch/client.go,internal/fetch/fetch/backoff.go,internal/fetch/fetch/honest.go,internal/fetch/fetch/charset.go,internal/fetch/fetch/guard.go,internal/fetch/robots/robots.go,internal/fetch/archive/wayback.go,internal/fetch/relevance/relevance.go,internal/fetch/pipeline/truncate.go
PORT_SESSION_FIXTURE := testdata/port/session/lifecycle.json
PORT_SESSION_SOURCES := internal/session/lifecycle.go,internal/session/errors.go
PORT_WORKFLOW_FIXTURE := testdata/port/workflows/workflows.json
PORT_WORKFLOW_SOURCES := internal/flows/schema.go,internal/flows/runner.go,internal/flows/record.go,internal/journal/journal.go,internal/oob/oob.go,internal/policy/policy.go,internal/session/lifecycle.go,internal/session/errors.go,internal/trace/trace.go
PORT_FIXTURE_SOURCES := internal/output/output.go,internal/output/human_renderers.go,internal/output/codes.go,internal/budget/budget.go,internal/config/config.go,internal/config/show.go,cmd/symbrowse/batch.go,internal/policy/allowlist.go,internal/policy/ssrf.go,internal/policy/policy.go,internal/policy/symguard.go,internal/state/store.go,internal/state/codec.go,internal/state/codec_select.go,internal/state/crypto.go,internal/state/keyresolver.go,internal/state/keychain_result.go,internal/state/vault.go,internal/engine/storage.go,internal/injection/scan.go,internal/injection/boundary.go,internal/injection/patterns.txt
PORT_BENCH_REPORT ?= port/results/go-baseline.json
PORT_VALUE_SIGNAL ?= docs/rust-port/value-signal-version.json
RUST_BINARY := target/debug/symbrowse
PORT_CONTRACT_VERSION ?= v0.8.0

build:
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/symbrowse

test:
	CGO_ENABLED=$(CGO_ENABLED) $(GO) test $(GOFLAGS) -count=1 ./...

test-race:
	CGO_ENABLED=1 $(GO) test $(GOFLAGS) -race -count=1 ./...

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		printf '%s\n' 'golangci-lint not found; falling back to go vet'; \
		CGO_ENABLED=$(CGO_ENABLED) $(GO) vet ./...; \
	fi

fmt-check:
	@files="$$(git ls-files '*.go' ':!:vendor/*')"; \
	if [ -n "$$files" ]; then \
		gofmt_bin="$$(GOTOOLCHAIN=go$(GO_VERSION) $(GO) env GOROOT 2>/dev/null)/bin/gofmt"; \
		if [ ! -x "$$gofmt_bin" ]; then \
			printf '%s\n' 'gofmt for Go $(GO_VERSION) unavailable; falling back to the gofmt in PATH'; \
			gofmt_bin=gofmt; \
		fi; \
		unformatted="$$("$$gofmt_bin" -l $$files)"; \
		if [ -n "$$unformatted" ]; then \
			printf '%s\n' 'The following Go files are not formatted:'; \
			printf '%s\n' "$$unformatted"; \
			exit 1; \
		fi; \
	fi

# Build the pinned Go implementation used as the external Rust-port oracle.
# Migration-only files may change, but production Go inputs must still match
# the pinned commit until their contracts are deliberately re-frozen.
port-oracle-build:
	@git cat-file -e "$(PORT_ORACLE_COMMIT)^{commit}" 2>/dev/null || { \
		printf '%s\n' 'pinned Go oracle commit $(PORT_ORACLE_COMMIT) is unavailable'; \
		exit 1; \
	}
	@set -eu; \
		root="$$(pwd)"; worktree="$(PORT_ORACLE_WORKTREE)"; \
		if [ -e "$$worktree" ]; then \
			git -C "$$worktree" rev-parse --is-inside-work-tree >/dev/null 2>&1 || { printf '%s\n' "refusing to remove non-worktree $$worktree"; exit 1; }; \
			test -z "$$(git -C "$$worktree" symbolic-ref -q HEAD || true)" || { printf '%s\n' "refusing to remove branch worktree $$worktree"; exit 1; }; \
			test "$$(git -C "$$worktree" rev-parse HEAD)" = "$(PORT_ORACLE_COMMIT)" || { printf '%s\n' "refusing to remove worktree at another commit $$worktree"; exit 1; }; \
			test -z "$$(git -C "$$worktree" status --porcelain --untracked-files=all)" || { printf '%s\n' "refusing to remove dirty worktree $$worktree"; exit 1; }; \
			git worktree remove "$$worktree"; \
		fi; \
		git worktree add --detach "$$worktree" $(PORT_ORACLE_COMMIT) >/dev/null; \
		trap 'git worktree remove "$$worktree" >/dev/null 2>&1 || true' EXIT HUP INT TERM; \
		test -z "$$(git -C "$$worktree" status --porcelain --untracked-files=all)"; \
		mkdir -p "$$root/target/port"; \
		cd "$$worktree"; \
		GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) build -trimpath \
			-ldflags "-s -w -X main.version=$(PORT_ORACLE_RELEASE)" \
			-o "$$root/$(PORT_GO_BINARY)" ./cmd/symbrowse; \
		test -z "$$(git status --porcelain --untracked-files=all)"

differential-go-selftest: port-oracle-build
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/diffharness \
		--left ./$(PORT_GO_BINARY) --right ./$(PORT_GO_BINARY) \
		--cases $(PORT_CASES) \
		--expect-oracle-commit $(PORT_ORACLE_COMMIT) \
		--expect-oracle-release $(PORT_ORACLE_RELEASE) \
		--verify-left-go-revision

port-fixture-source-check:
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/sourcecheck \
		--oracle $(PORT_ORACLE_COMMIT) --paths $(PORT_FIXTURE_SOURCES)

port-core-fixtures-generate: port-fixture-source-check
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/coregen \
		--output $(PORT_CORE_FIXTURE)

port-core-fixtures-check: port-fixture-source-check
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/coregen \
		--check --output $(PORT_CORE_FIXTURE)

port-config-fixtures-generate: port-fixture-source-check
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/configgen \
		--output $(PORT_CONFIG_FIXTURE)

port-config-fixtures-check: port-fixture-source-check
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/configgen \
		--check --output $(PORT_CONFIG_FIXTURE)

port-policy-fixtures-generate: port-fixture-source-check
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/policygen \
		--output $(PORT_POLICY_FIXTURE)

port-policy-fixtures-check: port-fixture-source-check
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/policygen \
		--check --output $(PORT_POLICY_FIXTURE)

port-state-fixtures-generate: port-fixture-source-check
	SYMBROWSE_PORT_STATE_FIXTURE_DIR="$(CURDIR)/$(PORT_STATE_FIXTURE_DIR)" \
		SYMBROWSE_PORT_FIXTURE_UPDATE=1 GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 \
		$(GO) test ./internal/state -run '^TestGeneratePortStateFixtures$$' -count=1

port-state-fixtures-check: port-fixture-source-check
	SYMBROWSE_PORT_STATE_FIXTURE_DIR="$(CURDIR)/$(PORT_STATE_FIXTURE_DIR)" \
		GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 \
		$(GO) test ./internal/state -run '^TestGeneratePortStateFixtures$$' -count=1

port-mcp-fixtures-generate: port-oracle-build
	python3 scripts/rust-port/mcp_fixture_gen.py --oracle $(PORT_GO_BINARY)

port-mcp-fixtures-check: port-oracle-build
	python3 scripts/rust-port/mcp_fixture_gen.py --oracle $(PORT_GO_BINARY) --check

port-engine-fixtures-generate:
	python3 scripts/rust-port/engine_fixture_gen.py
	python3 scripts/rust-port/file_guard_fixture_gen.py

port-engine-fixtures-check:
	python3 scripts/rust-port/engine_fixture_gen.py --check
	python3 scripts/rust-port/file_guard_fixture_gen.py --check

port-injection-fixtures-generate:
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/sourcecheck \
		--oracle $(PORT_ORACLE_COMMIT) --paths $(PORT_INJECTION_SOURCES)
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/injectiongen \
		--output $(PORT_INJECTION_FIXTURE)

port-injection-fixtures-check:
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/sourcecheck \
		--oracle $(PORT_ORACLE_COMMIT) --paths $(PORT_INJECTION_SOURCES)
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/injectiongen \
		--check --output $(PORT_INJECTION_FIXTURE)

port-fetch-static-fixtures-generate:
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/sourcecheck \
		--oracle $(PORT_ORACLE_COMMIT) --paths $(PORT_FETCH_STATIC_SOURCES)
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/fetch_fixture_gen.go \
		--out $(PORT_FETCH_STATIC_FIXTURE)

port-fetch-static-fixtures-check:
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/sourcecheck \
		--oracle $(PORT_ORACLE_COMMIT) --paths $(PORT_FETCH_STATIC_SOURCES)
	@set -eu; tmp="$$(mktemp)"; trap 'rm -f "$$tmp"' EXIT HUP INT TERM; \
		GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/fetch_fixture_gen.go --out "$$tmp"; \
		cmp -s "$$tmp" $(PORT_FETCH_STATIC_FIXTURE) || { printf '%s\n' 'FAIL static fetch fixture drift'; exit 1; }

port-fetch-control-fixtures-generate:
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/sourcecheck \
		--oracle $(PORT_ORACLE_COMMIT) --paths $(PORT_FETCH_CONTROL_SOURCES)
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run -tags rustport ./scripts/rust-port/cmd/fetchcontrolgen \
		--out $(PORT_FETCH_CONTROL_FIXTURE)

port-fetch-control-fixtures-check:
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/sourcecheck \
		--oracle $(PORT_ORACLE_COMMIT) --paths $(PORT_FETCH_CONTROL_SOURCES)
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run -tags rustport ./scripts/rust-port/cmd/fetchcontrolgen \
		--check --out $(PORT_FETCH_CONTROL_FIXTURE)

port-session-fixture-generate:
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/sourcecheck \
		--oracle $(PORT_ORACLE_COMMIT) --paths $(PORT_SESSION_SOURCES)
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/sessiongen \
		--output $(PORT_SESSION_FIXTURE)

port-session-fixture-check:
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/sourcecheck \
		--oracle $(PORT_ORACLE_COMMIT) --paths $(PORT_SESSION_SOURCES)
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/sessiongen \
		--check --output $(PORT_SESSION_FIXTURE)

port-workflow-fixture-generate:
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/sourcecheck \
		--oracle $(PORT_ORACLE_COMMIT) --paths $(PORT_WORKFLOW_SOURCES)
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/workflowgen \
		--output $(PORT_WORKFLOW_FIXTURE)

port-workflow-fixture-check:
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/sourcecheck \
		--oracle $(PORT_ORACLE_COMMIT) --paths $(PORT_WORKFLOW_SOURCES)
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/workflowgen \
		--check --output $(PORT_WORKFLOW_FIXTURE)

port-benchmark: port-oracle-build
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/portbench \
		--binary ./$(PORT_GO_BINARY) --output $(PORT_BENCH_REPORT)

port-value-signal: port-oracle-build
	SYMBROWSE_VERSION=$(PORT_CONTRACT_VERSION) $(CARGO) build --release -p symbrowse-cli --bin symbrowse --locked
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/portbench \
		--binary ./$(PORT_GO_BINARY) --candidate ./target/release/symbrowse \
		--workload version-json --runs 30 --output $(PORT_VALUE_SIGNAL)

port-contract: port-oracle-build
	python3 docs/rust-port/validate.py
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) test -count=1 ./scripts/rust-port/...
	$(MAKE) port-core-fixtures-check
	$(MAKE) port-config-fixtures-check
	$(MAKE) port-policy-fixtures-check
	$(MAKE) differential-go-selftest
	$(MAKE) port-benchmark

rust-build:
	$(CARGO) build --workspace --locked

rust-check:
	$(CARGO) check --workspace --all-targets --all-features --locked

rust-lint:
	$(CARGO) fmt --all --check
	$(CARGO) clippy --workspace --all-targets --all-features --locked -- -D warnings

rust-test:
	$(CARGO) nextest run --workspace --all-features --locked
	$(CARGO) test --workspace --doc --all-features --locked

rust-features:
	$(CARGO) hack check --workspace --each-feature --no-dev-deps --locked

rust-security:
	$(CARGO) audit
	$(CARGO) deny check

rust-version-contract: port-oracle-build
	SYMBROWSE_VERSION=$(PORT_CONTRACT_VERSION) $(CARGO) build -p symbrowse-cli --bin symbrowse --locked
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/diffharness \
		--left ./$(PORT_GO_BINARY) --right ./$(RUST_BINARY) \
		--cases $(PORT_CASES) --stage version \
		--expect-oracle-commit $(PORT_ORACLE_COMMIT) \
		--expect-oracle-release $(PORT_ORACLE_RELEASE) \
		--verify-left-go-revision

rust-core-contract: port-oracle-build port-core-fixtures-check port-config-fixtures-check
	$(CARGO) test -p symbrowse-core --all-features --locked
	SYMBROWSE_VERSION=$(PORT_CONTRACT_VERSION) $(CARGO) build -p symbrowse-cli --bin symbrowse --locked
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/diffharness \
		--left ./$(PORT_GO_BINARY) --right ./$(RUST_BINARY) \
		--cases $(PORT_CASES) --stage config-output \
		--expect-oracle-commit $(PORT_ORACLE_COMMIT) \
		--expect-oracle-release $(PORT_ORACLE_RELEASE) \
		--verify-left-go-revision
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/diffharness \
		--left ./$(PORT_GO_BINARY) --right ./$(RUST_BINARY) \
		--cases $(PORT_CASES) --stage batch \
		--expect-oracle-commit $(PORT_ORACLE_COMMIT) \
		--expect-oracle-release $(PORT_ORACLE_RELEASE) \
		--verify-left-go-revision

rust-state-contract: port-state-fixtures-check
	$(CARGO) test -p symbrowse-core --all-features --locked

rust-policy-contract: port-policy-fixtures-check
	$(CARGO) test -p symbrowse-core --test policy_contract --all-features --locked

rust-mcp-contract: port-mcp-fixtures-check
	$(CARGO) test -p symbrowse-mcp --all-features --locked
	SYMBROWSE_VERSION=$(PORT_CONTRACT_VERSION) $(CARGO) build -p symbrowse-cli --bin symbrowse --locked

rust-engine-contract: port-engine-fixtures-check
	$(CARGO) test -p symbrowse-engine --all-features --locked

rust-injection-contract: port-injection-fixtures-check
	$(CARGO) test -p symbrowse-core --test injection_contract --all-features --locked

rust-fetch-static-slice: port-fetch-static-fixtures-check
	$(CARGO) test -p symbrowse-fetch --test render_corpus --test static_controls --all-features --locked

rust-fetch-contract: port-fetch-static-fixtures-check port-fetch-control-fixtures-check
	$(CARGO) test -p symbrowse-fetch --all-targets --all-features --locked
	python3 port/harness/run.py --suite fetch-control
	python3 port/harness/run.py --suite fetch-render --comparison bytes
	python3 scripts/rust-port/check_fetch_case_ids.py

rust-session-contract: port-session-fixture-check
	$(CARGO) test -p symbrowse-core --test session_lifecycle --all-features --locked

rust-workflow-slice: port-workflow-fixture-check
	$(CARGO) test -p symbrowse-core --test workflows_contract --all-features --locked

rust-safari-slice:
	$(CARGO) test -p symbrowse-engine-safari --all-targets --all-features --locked

rust-browser-contract:
	python3 scripts/rust-port/browser_fixture_gen.py --suite all --check
	$(CARGO) test -p symbrowse-engine-chrome --test contract_fixture --locked
	$(CARGO) test -p symbrowse-engine-safari --test contract_fixture --locked
	$(CARGO) test -p symbrowse-engine-firefox --lib --locked

rust-native-browser-contract:
	python3 port/harness/run.py --suite all --native-targets

port-daemon-fixture-generate:
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/daemongen --output $(PORT_DAEMON_FIXTURE)

port-daemon-fixture-check:
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/sourcecheck \
		--oracle $(PORT_ORACLE_COMMIT) --paths $(PORT_DAEMON_SOURCES)
	GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 $(GO) run ./scripts/rust-port/cmd/daemongen --check --output $(PORT_DAEMON_FIXTURE)

rust-daemon-contract: port-daemon-fixture-check
	$(CARGO) test -p symbrowse-daemon --all-features --locked
	$(CARGO) install --path crates/symbrowse-cli --root target/port/rust-install --locked --force
	SYMBROWSE_RUST_BINARY="$(CURDIR)/target/port/rust-install/bin/symbrowse$(if $(filter Windows_NT,$(OS)),.exe,)" python3 port/harness/run.py --suite daemon

rust-cdp-spike:
	python3 scripts/rust-port/validate_cdp_spike.py
	$(CARGO) test -p symbrowse-engine-chrome --all-targets --locked

rust-miri:
	scripts/rust_miri.sh

rust-fuzz-smoke:
	scripts/fuzz_smoke.sh

rust-inventory:
	python3 scripts/rust_hardening_inventory.py --check
	python3 scripts/fuzz_corpus_hashes.py --check

rust-hardening: rust-miri rust-fuzz-smoke rust-inventory
	$(CARGO) hack check --workspace --each-feature --no-dev-deps
	$(CARGO) audit --file fuzz/Cargo.lock
	$(CARGO) deny --manifest-path fuzz/Cargo.toml check
	scripts/rust_geiger.sh

rust-release-gates:
	python3 port/release/verify.py --self-test

rust-gates:
	$(MAKE) rust-lint
	$(MAKE) rust-check
	$(MAKE) rust-test
	$(MAKE) rust-features
	$(MAKE) rust-security
	$(MAKE) rust-version-contract
	$(MAKE) rust-core-contract
	$(MAKE) rust-policy-contract
	$(MAKE) rust-state-contract
	$(MAKE) rust-mcp-contract
	$(MAKE) rust-engine-contract
	$(MAKE) rust-injection-contract
	$(MAKE) rust-fetch-static-slice
	$(MAKE) rust-fetch-contract
	$(MAKE) rust-session-contract
	$(MAKE) rust-workflow-slice
	$(MAKE) rust-safari-slice
	$(MAKE) rust-browser-contract
	$(MAKE) rust-daemon-contract
	$(MAKE) rust-cdp-spike

clean:
	rm -rf $(BINARY) dist coverage.out target/port port/results
