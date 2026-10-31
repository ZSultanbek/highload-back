// Command bench measures gRPC (HTTP/2 + protobuf) against the REST/JSON
// (HTTP/1.1) baseline for the ImageService: latency percentiles and on-the-wire
// payload sizes. It also demonstrates the three reliability features required
// by SIS #2 — idempotency, timeouts and retries.
//
// Everything runs in-process over loopback, so the numbers isolate protocol
// overhead (serialization + transport framing), not real network RTT. That is
// the LEAST favourable setup for gRPC (no network means its transfer-time win
// nearly vanishes), so any win it still shows here only widens in production.
//
//	go run ./cmd/bench -n 20000 -warmup 2000 -concurrency 1 -list-size 50
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	imagev1 "github.com/ZSultanbek/highload-back/sis2/gen/image/v1"
	"github.com/ZSultanbek/highload-back/sis2/internal/grpcserver"
	"github.com/ZSultanbek/highload-back/sis2/internal/restserver"
	"github.com/ZSultanbek/highload-back/sis2/internal/retry"
	"github.com/ZSultanbek/highload-back/sis2/internal/service"
	"github.com/ZSultanbek/highload-back/sis2/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func main() {
	n := flag.Int("n", 20000, "measured requests per protocol")
	warmup := flag.Int("warmup", 2000, "warmup requests (excluded)")
	concurrency := flag.Int("concurrency", 1, "concurrent client goroutines")
	listSize := flag.Int("list-size", 50, "records per ListImages payload-size sample")
	flag.Parse()

	restAddr := "127.0.0.1:18080"
	grpcAddr := "127.0.0.1:18081"

	// --- start both servers in-process (separate stores) ---
	restSvc := service.New(store.New())
	startREST(restAddr, restSvc)

	grpcSvc := service.New(store.New())
	startGRPC(grpcAddr, grpcSvc)

	waitListen(restAddr)
	waitListen(grpcAddr)

	// --- clients ---
	// Configure HTTP/1.1 keep-alive so the REST baseline reuses connections
	// (fair vs gRPC's single multiplexed HTTP/2 connection). Without this the
	// default pool of 2 idle conns/host forces a new TCP handshake per call and
	// exhausts ephemeral ports under concurrency.
	tr := &http.Transport{
		MaxIdleConns:        *concurrency + 16,
		MaxIdleConnsPerHost: *concurrency + 16,
		MaxConnsPerHost:     *concurrency + 16,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   false, // keep the baseline on HTTP/1.1
	}
	httpc := &http.Client{Timeout: 5 * time.Second, Transport: tr}
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("grpc dial: %v", err)
	}
	defer conn.Close()
	gc := imagev1.NewImageServiceClient(conn)

	ctx := context.Background()

	// --- seed one known image in each server ---
	restID := restSeedOne(ctx, httpc, restAddr)
	grpcImg, err := gc.CreateImage(ctx, sampleCreate())
	if err != nil {
		log.Fatalf("grpc seed: %v", err)
	}
	grpcID := grpcImg.GetId()

	fmt.Printf("SIS #2 — gRPC vs REST/JSON benchmark\n")
	fmt.Printf("n=%d warmup=%d concurrency=%d list-size=%d (loopback)\n\n", *n, *warmup, *concurrency, *listSize)

	// ================= PAYLOAD SIZE =================
	payloadSizes(grpcImg, *listSize)

	// ================= LATENCY: GetImage =================
	grpcLat := measure(*n, *warmup, *concurrency, func() error {
		c, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_, err := gc.GetImage(c, &imagev1.GetImageRequest{Id: grpcID})
		return err
	})
	restLat := measure(*n, *warmup, *concurrency, func() error {
		return restGet(ctx, httpc, restAddr, restID)
	})

	printLatency(grpcLat, restLat)

	// ================= RELIABILITY DEMOS =================
	demoIdempotency(ctx, gc)
	demoTimeout(ctx, gc, grpcID)
	demoRetry()
}

// ---------- payload size ----------

func payloadSizes(img *imagev1.Image, listSize int) {
	pb, _ := proto.Marshal(img)
	js, _ := json.Marshal(protoToJSONMap(img))

	// a list of N records
	list := &imagev1.ListImagesResponse{NextCursor: "abc"}
	var jsonList []map[string]any
	for i := 0; i < listSize; i++ {
		list.Images = append(list.Images, img)
		jsonList = append(jsonList, protoToJSONMap(img))
	}
	pbList, _ := proto.Marshal(list)
	jsList, _ := json.Marshal(map[string]any{"images": jsonList, "next_cursor": "abc"})

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "PAYLOAD SIZE (bytes)\tprotobuf\tJSON\tJSON/proto")
	fmt.Fprintf(w, "  single Image (response)\t%d\t%d\t%.2fx\n", len(pb), len(js), ratio(len(js), len(pb)))
	fmt.Fprintf(w, "  ListImages x%d\t%d\t%d\t%.2fx\n", listSize, len(pbList), len(jsList), ratio(len(jsList), len(pbList)))
	fmt.Fprintln(w)
	w.Flush()
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func protoToJSONMap(img *imagev1.Image) map[string]any {
	return map[string]any{
		"id":                 img.GetId(),
		"owner_id":           img.GetOwnerId(),
		"bucket":             img.GetBucket(),
		"object_key":         img.GetObjectKey(),
		"content_type":       img.GetContentType(),
		"size_bytes":         img.GetSizeBytes(),
		"width":              img.GetWidth(),
		"height":             img.GetHeight(),
		"checksum":           img.GetChecksum(),
		"status":             img.GetStatus().String(),
		"created_at_unix_ms": img.GetCreatedAtUnixMs(),
	}
}

// ---------- latency ----------

type stats struct {
	p50, p95, p99, max time.Duration
	rps                float64
}

func measure(n, warmup, concurrency int, call func() error) stats {
	// warmup
	for i := 0; i < warmup; i++ {
		_ = call()
	}
	durs := make([]time.Duration, n)
	start := time.Now()
	if concurrency <= 1 {
		for i := 0; i < n; i++ {
			t0 := time.Now()
			if err := call(); err != nil {
				log.Fatalf("call failed: %v", err)
			}
			durs[i] = time.Since(t0)
		}
	} else {
		type job struct{ idx int }
		jobs := make(chan job, concurrency)
		done := make(chan struct{})
		for w := 0; w < concurrency; w++ {
			go func() {
				for j := range jobs {
					t0 := time.Now()
					if err := call(); err != nil {
						log.Fatalf("call failed: %v", err)
					}
					durs[j.idx] = time.Since(t0)
				}
				done <- struct{}{}
			}()
		}
		for i := 0; i < n; i++ {
			jobs <- job{idx: i}
		}
		close(jobs)
		for w := 0; w < concurrency; w++ {
			<-done
		}
	}
	elapsed := time.Since(start)
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	return stats{
		p50: pct(durs, 50),
		p95: pct(durs, 95),
		p99: pct(durs, 99),
		max: durs[len(durs)-1],
		rps: float64(n) / elapsed.Seconds(),
	}
}

func pct(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p * len(sorted)) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func printLatency(g, r stats) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "LATENCY GetImage\tp50\tp95\tp99\tmax\tthroughput")
	fmt.Fprintf(w, "  gRPC (HTTP/2+proto)\t%s\t%s\t%s\t%s\t%.0f req/s\n", us(g.p50), us(g.p95), us(g.p99), us(g.max), g.rps)
	fmt.Fprintf(w, "  REST (HTTP/1.1+JSON)\t%s\t%s\t%s\t%s\t%.0f req/s\n", us(r.p50), us(r.p95), us(r.p99), us(r.max), r.rps)
	fmt.Fprintln(w)
	w.Flush()
}

func us(d time.Duration) string {
	return fmt.Sprintf("%.1fµs", float64(d.Nanoseconds())/1000.0)
}

// ---------- reliability demos ----------

func demoIdempotency(ctx context.Context, gc imagev1.ImageServiceClient) {
	req := &imagev1.CreateImageRequest{
		OwnerId: "11111111-1111-1111-1111-111111111111", ContentType: "image/png",
		SizeBytes: 1234, Checksum: "deadbeef", IdempotencyKey: "fixed-key-123",
	}
	a, _ := gc.CreateImage(ctx, req)
	b, _ := gc.CreateImage(ctx, req) // retry with same key
	ok := a.GetId() == b.GetId()
	fmt.Printf("RELIABILITY idempotency: two creates w/ same key -> same id? %v (%s)\n", ok, a.GetId())
}

func demoTimeout(ctx context.Context, gc imagev1.ImageServiceClient, id string) {
	// 1ns deadline: the call cannot complete in time -> DeadlineExceeded.
	c, cancel := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel()
	_, err := gc.GetImage(c, &imagev1.GetImageRequest{Id: id})
	code := status.Code(err)
	fmt.Printf("RELIABILITY timeout: 1ns deadline -> code=%s (expected DeadlineExceeded)\n", code)
}

func demoRetry() {
	// Start a gRPC server that fails ~60% of calls, then show the retry policy
	// recovers where a single attempt would often fail.
	addr := "127.0.0.1:18082"
	startGRPC2(addr, service.NewWithFailRate(store.New(), 0.6))
	waitListen(addr)
	conn, _ := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer conn.Close()
	gc := imagev1.NewImageServiceClient(conn)
	ctx := context.Background()

	seed, err := gc.CreateImage(ctx, sampleCreate())
	for err != nil { // seed through the flaky server
		seed, err = gc.CreateImage(ctx, sampleCreate())
	}
	id := seed.GetId()

	isRetryable := func(err error) bool {
		c := status.Code(err)
		return c == codes.Unavailable || c == codes.DeadlineExceeded
	}

	const trials = 200
	singleOK, retryOK := 0, 0
	for i := 0; i < trials; i++ {
		if _, err := gc.GetImage(ctx, &imagev1.GetImageRequest{Id: id}); err == nil {
			singleOK++
		}
		err := retry.Do(ctx, retry.Default(), isRetryable, func(c context.Context) error {
			_, e := gc.GetImage(c, &imagev1.GetImageRequest{Id: id})
			return e
		})
		if err == nil {
			retryOK++
		}
	}
	fmt.Printf("RELIABILITY retry (60%% fault): single-attempt %d/%d ok, with-retry %d/%d ok\n",
		singleOK, trials, retryOK, trials)
}

// ---------- server/client plumbing ----------

func startREST(addr string, svc *service.Service) {
	srv := restserver.New(svc)
	go func() { _ = http.ListenAndServe(addr, srv.Handler()) }()
}

func startGRPC(addr string, svc *service.Service)  { startGRPCNamed(addr, svc) }
func startGRPC2(addr string, svc *service.Service) { startGRPCNamed(addr, svc) }

func startGRPCNamed(addr string, svc *service.Service) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("grpc listen %s: %v", addr, err)
	}
	s := grpc.NewServer()
	imagev1.RegisterImageServiceServer(s, grpcserver.New(svc))
	go func() { _ = s.Serve(lis) }()
}

func waitListen(addr string) {
	for i := 0; i < 100; i++ {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	log.Fatalf("server at %s did not come up", addr)
}

func sampleCreate() *imagev1.CreateImageRequest {
	return &imagev1.CreateImageRequest{
		OwnerId:        "123e4567-e89b-12d3-a456-426614174000",
		ContentType:    "image/jpeg",
		SizeBytes:      512000,
		Checksum:       "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		IdempotencyKey: fmt.Sprintf("seed-%d", time.Now().UnixNano()),
	}
}

func restSeedOne(ctx context.Context, c *http.Client, addr string) string {
	body := map[string]any{
		"owner_id":        "123e4567-e89b-12d3-a456-426614174000",
		"content_type":    "image/jpeg",
		"size_bytes":      512000,
		"checksum":        "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"idempotency_key": fmt.Sprintf("seed-%d", time.Now().UnixNano()),
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/v1/images", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		log.Fatalf("rest seed: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.ID
}

func restGet(ctx context.Context, c *http.Client, addr, id string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/v1/images/"+id, nil)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// drain so the connection is reusable (keep-alive)
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
