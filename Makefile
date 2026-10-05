WAILS = go run github.com/wailsapp/wails/v2/cmd/wails@v2.15.0

.PHONY: dev build test frontend check bindings

dev:
	$(WAILS) dev

build:
	$(WAILS) build

frontend:
	cd frontend && npm ci && npm run build

bindings:
	$(WAILS) generate module

test:
	go test -race ./...

check: frontend test
	go vet ./...
