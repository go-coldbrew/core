.PHONY: build test doc
build:
	go build ./...

test:
	go test ./... -race

install:
	go install github.com/golangci/golangci-lint/cmd/golangci-lint \
		github.com/princjef/gomarkdoc/cmd/gomarkdoc

lint: install
	golangci-lint run

doc: install
	gomarkdoc ./...
