.PHONY: build test smoke check tui tui-fixtures dev dev-down dev-clean install uninstall

PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

build:
	go build -o bin/wtable ./cmd/wtable

install: build
	mkdir -p "$(BINDIR)"
	install -m 0755 bin/wtable "$(BINDIR)/wtable"

uninstall:
	rm -f "$(BINDIR)/wtable"

test:
	go test -race ./internal/terminal ./internal/agent ./internal/server

# Isolated TUI for manual work. `make tui` shows only real installed agents;
# `make tui-fixtures` also lists the deterministic dev-* fixtures.
tui: build
	./scripts/dev.sh tui --real

tui-fixtures: build
	./scripts/dev.sh tui

# Start the daemon without attaching (for a TUI opened separately).
dev: build
	./scripts/dev.sh up

dev-down:
	./scripts/dev.sh down

dev-clean:
	./scripts/dev.sh clean

smoke: build
	python3 scripts/smoke.py --binary bin/wtable --output .dev/test-results/smoke.json
	python3 scripts/p0_smoke.py --binary bin/wtable --output .dev/test-results/p0.json
	python3 scripts/p1_smoke.py --binary bin/wtable --output .dev/test-results/p1.json
	python3 scripts/p2_smoke.py --binary bin/wtable --output .dev/test-results/p2.json
	python3 scripts/scheduler_smoke.py --binary bin/wtable --output .dev/test-results/scheduler.json
	python3 scripts/view_smoke.py --binary bin/wtable --output .dev/test-results/view.json
	python3 scripts/first_run_smoke.py --binary bin/wtable --output .dev/test-results/first-run.json
	python3 scripts/workspace_smoke.py --binary bin/wtable --output .dev/test-results/workspace.json

check:
	go vet ./...
