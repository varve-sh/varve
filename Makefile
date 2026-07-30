.PHONY: build test lint clean install dogfood snapshot release tidy

BINARY   = varve
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS  = -ldflags "-s -w -X main.version=$(VERSION)"

build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/varve

test:
	VARVE_EMBED_PROVIDER=disabled go test ./... -count=1

lint:
	go vet ./...

clean:
	rm -rf bin/ dist/

install: build
	@mkdir -p $(shell go env GOPATH)/bin
	cp bin/$(BINARY) $(shell go env GOPATH)/bin/$(BINARY)
	@command -v $(BINARY) >/dev/null 2>&1 || printf '\ninstalled to %s/bin/%s, but that directory is not on your PATH.\nadd it:  export PATH="$$PATH:%s/bin"\n\n' "$(shell go env GOPATH)" "$(BINARY)" "$(shell go env GOPATH)"

# One command to run before dogfooding, because dogfooding exercises the
# INSTALLED binary and nothing otherwise connects that to your working tree.
#
# Cost of not having this, measured: a fix was committed and pushed but not
# installed, the next `varve migrate --from-v1` ran on a binary one commit stale,
# the behaviour under test was silently absent, and the missing behaviour looked
# like a code bug rather than a stale install.
dogfood: install
	@printf '\n'
	@installed="$$($(shell go env GOPATH)/bin/$(BINARY) --version 2>/dev/null | awk '{print $$NF}')"; \
	head="$(VERSION)"; \
	printf 'installed  %s\n' "$$installed"; \
	printf 'HEAD       %s\n' "$$head"; \
	if [ "$$installed" != "$$head" ]; then \
		printf '\nMISMATCH — the installed binary is not this tree. Do not trust a dogfood run.\n'; \
		exit 1; \
	fi; \
	case "$$head" in *-dirty) \
		printf '\nnote: working tree is dirty, so you are exercising uncommitted changes.\n' ;; \
	esac
	@command -v $(BINARY) >/dev/null 2>&1 \
		|| printf '\nnote: %s is not on your PATH; invoke it as %s/bin/%s\n' \
			"$(BINARY)" "$(shell go env GOPATH)" "$(BINARY)"
	@printf '\n'
	@# Which store you are about to exercise, resolved rather than assumed — a
	@# pre-rename store is reported here, not discovered halfway through a run.
	@$(shell go env GOPATH)/bin/$(BINARY) status 2>/dev/null | sed -n '1,5p' \
		|| printf 'no varve project here — run `%s init` in the directory you want to exercise\n' "$(BINARY)"

tidy:
	go mod tidy

# Build all platforms locally without publishing (requires goreleaser)
snapshot:
	goreleaser release --snapshot --clean

# Tag and push a release — triggers the release workflow
# Usage: make release VERSION=1.2.3
release:
	@test -n "$(VERSION)" || (echo "usage: make release VERSION=x.y.z" && exit 1)
	git tag -a v$(VERSION) -m "Release v$(VERSION)"
	git push origin v$(VERSION)
