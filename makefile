.PHONY: build build-darwin-arm64 vet test

build:
	go build -o cisco-socks-server ./cmd

build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 go build -o cisco-socks-server ./cmd

vet:
	go vet ./...

test:
	go test ./...
