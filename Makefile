.PHONY: build test vet check

build:
	go build ./...
	go build -o bp ./cmd/bp

test:
	go test ./...

vet:
	go vet ./...

check: test vet build
