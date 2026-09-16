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
	@command -v bats >/dev/null 2>&1 || { \
		echo "bats not found — 'nix develop' provides it, or: nix run nixpkgs#bats -- tests/cli.bats"; \
		exit 1; }
	CCSWITCH_BIN=$(CURDIR)/bin/$(BIN) bats tests/cli.bats

.PHONY: check
check: vet test ## Vet + Go unit tests

.PHONY: conformance
conformance: vet test smoke ## Full regression gate: vet + Go tests + bats smoke (run before every change)

# Codesigning identity used by `make sign`. A self-signed certificate is
# enough — the point is a *stable* designated requirement, not trust.
SIGN_ID ?= ccswitch

.PHONY: sign
sign: build ## macOS: sign with a stable identity so keychain grants survive rebuilds
	@if [ "$$(uname)" != "Darwin" ]; then echo "sign: macOS only, skipping"; exit 0; fi
	@security find-certificate -c "$(SIGN_ID)" >/dev/null 2>&1 || { \
		echo "No '$(SIGN_ID)' certificate in your login keychain."; \
		echo "Create one once, in Keychain Access:"; \
		echo "  Certificate Assistant > Create a Certificate..."; \
		echo "  Name: $(SIGN_ID)   Identity Type: Self Signed Root"; \
		echo "  Certificate Type: Code Signing"; \
		exit 1; }
	codesign --force --sign "$(SIGN_ID)" \
		--identifier com.zach-source.ccswitch \
		--options runtime bin/$(BIN)
	@echo "Signed. Designated requirement:"
	@codesign -d -r- bin/$(BIN) 2>&1 | tail -1

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
