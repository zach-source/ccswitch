PREFIX  ?= /usr/local
BIN     := ccswitch

.PHONY: build
build: ## Compile the ccswitch binary into ./bin
	go build -ldflags "-s -w" -o bin/$(BIN) ./cmd/ccswitch

.PHONY: test
test: ## Run the Go test suite
	go test ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format all Go sources
	gofmt -w .

.PHONY: smoke
smoke: build ## Run the bats CLI smoke tests against the built binary
	CCSWITCH_BIN=$(CURDIR)/bin/$(BIN) bats tests/cli.bats

.PHONY: check
check: vet test ## Vet + unit tests

.PHONY: install
install: build ## Install the binary into $(PREFIX)/bin
	install -d $(DESTDIR)$(PREFIX)/bin
	install -m 755 bin/$(BIN) $(DESTDIR)$(PREFIX)/bin/$(BIN)

.PHONY: uninstall
uninstall: ## Remove the installed binary
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BIN)

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin result
