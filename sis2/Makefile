GOBIN := $(shell go env GOPATH)/bin
export PATH := $(PATH):$(GOBIN)

.PHONY: generate build test vet bench tools

tools:
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

generate:
	buf generate

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

bench:
	go run ./cmd/bench -n 20000 -warmup 2000 -concurrency 1 -list-size 50
	go run ./cmd/bench -n 40000 -warmup 4000 -concurrency 50 -list-size 50
