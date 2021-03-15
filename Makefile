.PHONY: build test doc
build:
	go build ./...

test:
	go test ./...

doc:
	go get github.com/princjef/gomarkdoc/cmd/gomarkdoc
	gomarkdoc ./...
