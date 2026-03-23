.PHONY: build test doc lint bench
build:
	go build ./...

test:
	go test -race ./...

doc:
	go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@latest
	gomarkdoc ./... > README_API.md
	cat README_PREAMBLE.md README_API.md > README.md
	rm README_API.md

lint:
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	golangci-lint run

bench:
	go test -run=^$ -bench=. -benchmem ./...
