.PHONY: build test smoke check

build:
	go build -o bin/wtable ./cmd/wtable

test:
	go test -race ./internal/terminal ./internal/agent

smoke: build
	python3 scripts/smoke.py --binary bin/wtable --output .dev/test-results/smoke.json

check:
	go vet ./...
