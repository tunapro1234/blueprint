.PHONY: build test vet check release

build:
	go build ./...
	go build -o bp ./cmd/bp

test:
	go test ./...

vet:
	go vet ./...

check: test vet build

# A release is signed and immutable. PUBLISH=1 exposes it after all artifacts exist.
release:
	@test -n "$(RELEASE_KEY)" || (echo 'Set RELEASE_KEY to the private Ed25519 signing key path'; exit 1)
	python3 scripts/publish-release.py --key "$(RELEASE_KEY)" $(if $(PUBLISH),--publish,)
