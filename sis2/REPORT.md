# SIS #2 — Protocol Implementation: gRPC vs REST/JSON

**Service:** T9 Image Hosting — image *metadata* interface (from SIS #1).
**Chosen protocol:** gRPC (HTTP/2 + Protocol Buffers). **Baseline:** REST/JSON (HTTP/1.1).
**Language:** Go 1.26.

Both protocols expose the *same* four operations (`CreateImage`, `GetImage`, `ListImages`, `DeleteImage`) and call the **same business logic and in-memory store**, so the measurements isolate the wire protocol (serialization + transport), not logic differences. Object bytes / S3 / CDN are out of scope for SIS #2 — this task is about the protocol carrying the metadata.

---

## 1. Hypotheses (stated before measuring)

- **H1 (payload):** protobuf encodes one `Image` in **significantly fewer bytes** than JSON — predicted 40–70% smaller.
- **H2 (tail latency):** gRPC has a **lower p99** than REST/JSON, because HTTP/2 multiplexing + binary parsing cut tail latency.
- **H3 (the nuance):** on **loopback with small payloads**, REST may win the **median (p50)** — gRPC's advantage concentrates in payload size and the tail, and would widen with larger payloads and real network RTT. This is the *least* favourable setup for gRPC (no network ⇒ its transfer-time win nearly vanishes), so any win it shows here only grows in production.

---

## 2. Method

- In-process servers over loopback; one gRPC client (single multiplexed HTTP/2 connection) and one REST client with **HTTP/1.1 keep-alive** (connection pool sized to the concurrency, so the baseline reuses connections and isn't penalised by per-call handshakes).
- Latency: `GetImage` on a pre-seeded id, `warmup` excluded, durations sorted → p50/p95/p99/max + throughput.
- Payload: the same `Image` serialized both ways (`proto.Marshal` vs `json.Marshal`), for a single record and a 50-record `ListImages`.
- Reproduce: `go run ./cmd/bench -n 20000 -warmup 2000 -concurrency 1` (and `-concurrency 50`).

> ⚠️ Loopback numbers measure CPU + framing overhead, **not** production latency (no network RTT, no TLS, single machine). Absolute µs vary run-to-run; the **relative** comparison is the result. Representative run below (Apple Silicon, Go 1.26).

---

## 3. Results

### 3.1 Payload size — H1

| Payload | protobuf | JSON | JSON / proto |
|---------|---------:|-----:|:------------:|
| single `Image` (response) | 201 B | 361 B | **1.80×** |
| `ListImages` × 50 | 10,205 B | 18,132 B | **1.78×** |

**→ H1 confirmed.** Protobuf is ~**44% smaller** (JSON is 1.8× larger). The savings come from binary field tags + varints vs JSON's repeated field names, quotes, and numbers-as-text. Deterministic, not noisy.

### 3.2 Latency `GetImage`, concurrency = 1

| Protocol | p50 | p95 | p99 | max | throughput |
|----------|----:|----:|----:|----:|-----------:|
| gRPC (HTTP/2 + proto) | 35.8 µs | 46.5 µs | 88.1 µs | 275.8 µs | 26.9k req/s |
| REST (HTTP/1.1 + JSON) | **30.7 µs** | **36.3 µs** | **49.3 µs** | 11,166 µs | **31.3k req/s** |

**→ At concurrency 1, REST wins** p50/p95/p99 and throughput. On loopback, gRPC's per-call HTTP/2 stream machinery costs more than it saves when there is no network transfer time to amortise it against. (REST's one 11 ms `max` is a GC/scheduling outlier.)

### 3.3 Latency `GetImage`, concurrency = 50

| Protocol | p50 | p95 | p99 | max | throughput |
|----------|----:|----:|----:|----:|-----------:|
| gRPC (HTTP/2 + proto) | 523 µs | **776.7 µs** | **933.4 µs** | **1,385 µs** | 96.2k req/s |
| REST (HTTP/1.1 + JSON) | **251 µs** | 915.8 µs | 1,195.5 µs | 2,587 µs | **147.2k req/s** |

**→ The tail flips under load.** gRPC wins **p95, p99, and max** — one multiplexed connection gives predictable, bounded tail latency with no per-connection head-of-line blocking. REST still wins **p50 and raw throughput** by spreading work across many TCP connections, but pays for it with a **worse tail** (p99 1.28× higher, max 1.87× higher).

---

## 4. Reliability features (required by the handout)

All three are implemented and demonstrated by the bench run:

| Feature | How | Observed |
|---------|-----|----------|
| **Idempotency** | `CreateImage` dedups on `idempotency_key` | two creates, same key → **same id** ✓ |
| **Timeouts** | `context.WithTimeout` deadline propagated client→server | 1 ns deadline → **`DeadlineExceeded`** ✓ |
| **Retries** | client wrapper: 3 attempts, exponential backoff + full jitter, only on `UNAVAILABLE`/`DEADLINE_EXCEEDED` | against a 60%-fault server: single-attempt ≈ **82/200** (≈ 1−0.6), with-retry ≈ **163/200** (≈ 1−0.6³) ✓ |

Retries are only applied to idempotent calls — `GET` is naturally safe, and `CreateImage` is safe because the idempotency key guarantees a retry never creates a duplicate.

---

## 5. Conclusions — hypotheses vs measured

| Hypothesis | Verdict |
|-----------|---------|
| **H1** protobuf much smaller | ✅ **Confirmed** — ~44% fewer bytes, consistently |
| **H2** gRPC better tail latency | 🟨 **Confirmed under concurrency** (p95/p99/max at c=50); **not** at c=1 on loopback |
| **H3** loopback favours REST on median | ✅ **Confirmed** — REST wins p50 + throughput in both runs |

**Why gRPC is still the right choice for this service** (the SIS #1 justification, now evidenced):
1. **Payload size is a guaranteed ~44% win** — directly cuts bandwidth and serialization cost, and matters most at the 2,000 RPS read load and across the internal network.
2. **Tail latency (p95/p99) is what the SLO targets** (`p99 < 100 ms`), and gRPC's multiplexing gives a **tighter, more predictable tail under concurrency** — exactly the regime a high-load service runs in.
3. The loopback median win for REST **shrinks or reverses with real network RTT**, where gRPC's smaller payloads and connection reuse pay off; the benchmark was deliberately gRPC's worst case.
4. gRPC provides **deadlines, status codes, and codegen'd contracts as first-class features**, so idempotency/timeouts/retries are native rather than hand-rolled.

**Where REST/JSON still wins:** human-readable, trivially debuggable (curl), browser-native, better single-connection median on a local machine — which is why it remains the right choice for public/browser-facing edges and stays as the baseline here.

### One-line takeaway
> Protobuf is unconditionally ~44% smaller; gRPC trades a bit of loopback median latency for a **tighter tail under load** and native reliability semantics — the right trade for an internal, SLO-bound, high-load metadata service, while REST/JSON stays the readable choice at the public edge.
