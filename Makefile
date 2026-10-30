.PHONY: build test smoke check

build:
	go build -o bin/wtable ./cmd/wtable

test:
	go test -race ./internal/terminal ./internal/agent ./internal/server

smoke: build
	python3 scripts/smoke.py --binary bin/wtable --output .dev/test-results/smoke.json
	python3 scripts/p0_smoke.py --binary bin/wtable --output .dev/test-results/p0.json

check:
	go vet ./...
