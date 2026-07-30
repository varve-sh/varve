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

# Install writes a NEW file and renames it over the old one, rather than cp-ing
# into the existing inode. On Apple Silicon, overwriting a Mach-O in place
# invalidates the ad-hoc signature the kernel has cached for that inode, and
# every subsequent run dies with SIGKILL — `varve --version` exits 137 with no
# output, on a binary that runs fine from bin/. That reads as a corrupt build or
# a broken command, not as an install-method bug. A rename gives the new binary
# its own inode, so the fresh signature is the one that gets checked.
install: build
	@mkdir -p $(shell go env GOPATH)/bin
	cp bin/$(BINARY) $(shell go env GOPATH)/bin/$(BINARY).new
	mv -f $(shell go env GOPATH)/bin/$(BINARY).new $(shell go env GOPATH)/bin/$(BINARY)
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
	@# The version guard above reads $(GOPATH)/bin/varve by absolute path, so it
	@# stays honest whatever PATH says. What it cannot see is which binary a bare
	@# `varve` actually runs — and since the v2.0.0 tap cutover a Homebrew install
	@# sits ahead of $(GOPATH)/bin on PATH. The two agree on the day you install
	@# and diverge on the next commit: dogfood reports a match, every command you
	@# type exercises the released build, and the behaviour under test is missing
	@# for the same reason as before. That is this target's own failure mode
	@# displaced by one step, so it is reported here rather than rediscovered.
	@resolved="$$(command -v $(BINARY) 2>/dev/null)"; \
	own="$(shell go env GOPATH)/bin/$(BINARY)"; \
	if [ -z "$$resolved" ]; then \
		printf '\nnote: %s is not on your PATH; invoke it as %s\n' "$(BINARY)" "$$own"; \
	elif [ ! "$$resolved" -ef "$$own" ]; then \
		other="$$("$$resolved" --version 2>/dev/null | awk '{print $$NF}')"; \
		printf '\nSHADOWED — a bare `%s` runs %s (%s),\n' "$(BINARY)" "$$resolved" "$$other"; \
		printf '           not the build just installed to %s (%s).\n' "$$own" "$(VERSION)"; \
		printf '           The binary you type is not the binary dogfood just verified.\n'; \
		printf '           Invoke by full path, or put %s/bin ahead on PATH.\n' "$(shell go env GOPATH)"; \
	fi
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
