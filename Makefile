.PHONY: build test vet check release

build:
	go build ./...
	go build -o bp ./cmd/bp

test:
	go test ./...

vet:
	go vet ./...

check: test vet build

release:
	mkdir -p site
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o site/bp-linux-amd64 ./cmd/bp
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o site/bp-linux-arm64 ./cmd/bp
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -o site/bp-darwin-amd64 ./cmd/bp
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -o site/bp-darwin-arm64 ./cmd/bp
