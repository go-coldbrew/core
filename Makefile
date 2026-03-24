.PHONY: build test doc lint bench
build:
	go build ./...

test:
	go test -race ./...

doc:
	go tool gomarkdoc ./...

lint:
	go tool golangci-lint run

bench:
	go test -run=^$ -bench=. -benchmem ./...
