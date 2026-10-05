WAILS = go run github.com/wailsapp/wails/v2/cmd/wails@v2.15.0

.PHONY: dev build test frontend check bindings format vet

# The frontend assets must exist before Go compiles main.go's embed directive.
# Keep separate requested targets ordered even when invoked with make -j.
.NOTPARALLEL:

dev:
	$(WAILS) dev

build:
	$(WAILS) build

frontend:
	node scripts/check.mjs frontend

bindings:
	$(WAILS) generate module

test: frontend
	go test -race ./...

vet: frontend
	go vet ./...

format:
	node scripts/check.mjs format

check:
	node scripts/check.mjs
