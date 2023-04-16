.PHONY: build test doc
build:
	go build ./...

test:
	go test ./... -race

doc:
	go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@latest
	gomarkdoc ./...
