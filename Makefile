.PHONY: build test doc lint bench
build:
	go build ./...

test:
	go test -race -cover ./...

doc:
	go tool gomarkdoc --output '{{.Dir}}/README.md' . ./config

lint:
	go tool golangci-lint run
	go tool govulncheck -scan=module

bench:
	go test -run=^$$ -bench=. -benchmem ./...
