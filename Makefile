.PHONY: build test vet check release rs-build rs-test rs-check e2e-go e2e-rs

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
	@test -z "$(PUBLISH)" -o "$(PUBLISH)" = 0 -o "$(PUBLISH)" = 1 || (echo 'PUBLISH must be 0 or 1'; exit 1)
	@test -n "$(RELEASE_KEY)" || (echo 'Set RELEASE_KEY to the private Ed25519 signing key path'; exit 1)
	python3 scripts/publish-release.py --key "$(RELEASE_KEY)" $(if $(filter 1,$(PUBLISH)),--publish,)

# Rust port (see PLAN.md). Builds run at low priority with a bounded job count.
CARGO ?= cargo
CARGO_BUILD_JOBS ?= 4
export CARGO_BUILD_JOBS

rs-build:
	nice -n 10 $(CARGO) build --release -p bp-cli

rs-test:
	nice -n 10 $(CARGO) test --workspace

rs-check:
	$(CARGO) fmt --all --check
	nice -n 10 $(CARGO) clippy --workspace --all-targets -- -D warnings
	nice -n 10 $(CARGO) test --workspace

e2e-go:
	python3 -m unittest scripts.test_local_cli

e2e-rs: rs-build
	BP_TEST_IMPL=rust BP_TEST_BINARY=target/release/bp python3 -m unittest scripts.test_local_cli
