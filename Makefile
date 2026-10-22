.PHONY: check lint test cover vuln run

check: lint test vuln

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...
	golangci-lint run ./...

test:
	go test -race -count=1 ./...

cover:
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

vuln:
	govulncheck ./...

run:
	go run ./cmd/shorturl
