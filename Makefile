.PHONY: build test doc lint bench
build:
	go build ./...

test:
	go test ./... -race

doc:
	go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@latest
	gomarkdoc ./...

lint:
	golangci-lint run

bench:
	go test -bench=. -benchmem ./...
