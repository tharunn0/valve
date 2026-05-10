.PHONY: test format lint

test:
	go test -v ./...

format:
	go fmt ./...

lint:
	go vet ./...
	golangci-lint run
