BINARY := bpm
CMD := ./cmd/bpm

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this list of commands
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the bpm binary into ./bin
	go build -o bin/$(BINARY) $(CMD)

.PHONY: install
install: ## Build bpm and install it to your Go bin dir (usually ~/go/bin) - adds bpm to your PATH
	go install $(CMD)
	@echo "Installed. Make sure $$(go env GOPATH)/bin is on your PATH, then run: bpm serve"

.PHONY: test
test: ## Run the test suite
	go test ./...

.PHONY: run
run: build ## Build and start the web UI on http://localhost:8080
	./bin/$(BINARY) serve --db bpm.db --addr :8080

.PHONY: tray
tray: ## Build and run the macOS menu bar app (auto-discovers your Bambu Studio dirs)
	go build -o bin/bpm-tray ./cmd/bpm-tray
	./bin/bpm-tray

.PHONY: docker
docker: ## Build the Docker image (see docker-compose.yml to run it)
	docker compose build

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin dist
