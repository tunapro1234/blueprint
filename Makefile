.PHONY: build test vet vuln check release

# Pinned; CI runs `make vuln` too. Exits nonzero when bp's code reaches a known
# vulnerability in the toolchain's standard library or a dependency.
GOVULNCHECK = golang.org/x/vuln/cmd/govulncheck@v1.8.0

build:
	go build ./...
	go build -o bp ./cmd/bp

test:
	go test ./...

vet:
	go vet ./...

vuln:
	go run $(GOVULNCHECK) ./...

check: test vet vuln build

# A release is signed and immutable. PUBLISH=1 exposes it after all artifacts exist.
release:
	@test -z "$(PUBLISH)" -o "$(PUBLISH)" = 0 -o "$(PUBLISH)" = 1 || (echo 'PUBLISH must be 0 or 1'; exit 1)
	@test -n "$(RELEASE_KEY)" || (echo 'Set RELEASE_KEY to the private Ed25519 signing key path'; exit 1)
	python3 scripts/publish-release.py --key "$(RELEASE_KEY)" $(if $(filter 1,$(PUBLISH)),--publish,)
