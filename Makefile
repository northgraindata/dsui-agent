VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint docker helm-lint

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dsui-agent ./cmd/dsui-agent

test:
	go vet ./...
	go test -race -count=1 ./...

lint:
	golangci-lint run

# Local image without GoReleaser: build a linux binary for the host arch and wrap it.
docker:
	rm -rf .docker-ctx && mkdir -p .docker-ctx/linux/$(shell go env GOARCH)
	CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "$(LDFLAGS)" -o .docker-ctx/linux/$(shell go env GOARCH)/dsui-agent ./cmd/dsui-agent
	cp Dockerfile .docker-ctx/Dockerfile
	docker build --platform linux/$(shell go env GOARCH) -t dsui-agent:local .docker-ctx
	rm -rf .docker-ctx

helm-lint:
	helm lint deploy/helm/dsui-agent
