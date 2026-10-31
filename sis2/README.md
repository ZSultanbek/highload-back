# SIS #2 — Protocol Implementation (gRPC vs REST/JSON) · Go

Implements the T9 Image-Hosting **metadata** service interface (from SIS #1) over
the chosen protocol **gRPC (HTTP/2 + protobuf)** and a **REST/JSON (HTTP/1.1)**
baseline, with **idempotency, timeouts and retries**, plus a benchmark of
**latency and payload size**.

See [`REPORT.md`](./REPORT.md) for the hypotheses, measurements, and conclusions.

## Layout

```
proto/image/v1/image.proto   service + message contract
gen/                         generated Go (buf) — checked in for convenience
internal/store/              in-memory, concurrency-safe metadata store
internal/service/            protocol-agnostic business logic (idempotency, pagination)
internal/grpcserver/         gRPC adapter  (service <-> protobuf)
internal/restserver/         REST adapter  (service <-> JSON)
internal/retry/              client retry policy: backoff + jitter + per-attempt timeout
cmd/grpc-server/             runnable gRPC server
cmd/rest-server/             runnable REST server
cmd/bench/                   in-process benchmark + reliability demos
```

Storage/S3/CDN are intentionally stubbed (in-memory) — SIS #2 is about the
**protocol**, not persistence. Both servers share identical logic so the
comparison isolates the wire protocol.

## Prerequisites

- Go 1.26+
- For regenerating protobuf (optional — generated code is committed):
  ```sh
  go install github.com/bufbuild/buf/cmd/buf@latest
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
  ```

## Usage

```sh
# regenerate protobuf (only if you change the .proto)
make generate      # or: buf generate

# build / test
make build
make test

# run the benchmark (starts both servers in-process)
make bench
# or directly, with knobs:
go run ./cmd/bench -n 20000 -warmup 2000 -concurrency 1  -list-size 50
go run ./cmd/bench -n 40000 -warmup 4000 -concurrency 50 -list-size 50
```

Run the servers standalone:

```sh
go run ./cmd/grpc-server -addr 127.0.0.1:8081
go run ./cmd/rest-server -addr 127.0.0.1:8080

# REST is curl-able:
curl -s -XPOST localhost:8080/v1/images \
  -d '{"owner_id":"o1","content_type":"image/jpeg","size_bytes":512000,"idempotency_key":"k1"}'
curl -s localhost:8080/v1/images/<id>
```

A `-fail-rate 0.5` flag on either server injects transient failures to exercise
the client retry policy.

> Benchmarks run over loopback, so they measure serialization + framing
> overhead, **not** production latency. The relative comparison is the result;
> absolute µs vary per run and machine.
