# highload-back

Backend / high-load coursework deliverables.

| Dir | Topic |
|-----|-------|
| [`sis2/`](./sis2) | SIS #2 — Protocol implementation (gRPC vs REST/JSON) in Go, with idempotency, timeouts, retries, and latency/payload-size measurements. |

Context topic: **T9 — Image Hosting (metadata)** — upload images to S3-compatible
storage, serve by link, with a metadata service (~2000 RPS read / 50 RPS upload,
20M files, p99 metadata < 100 ms).
