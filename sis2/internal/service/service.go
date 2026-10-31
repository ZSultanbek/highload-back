// Package service holds the protocol-agnostic business logic for the image
// metadata service. Both the gRPC and REST adapters call into this, so the
// benchmark isolates wire-protocol cost rather than logic differences.
package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ZSultanbek/highload-back/sis2/internal/store"
)

// Sentinel errors; adapters map these to gRPC codes / HTTP statuses.
var (
	ErrInvalidArgument = errors.New("invalid argument")
	ErrNotFound        = errors.New("not found")
)

// Service implements the image metadata operations.
type Service struct {
	store *store.Store

	idemMu sync.Mutex
	idem   map[string]string // idempotency_key -> image id

	// failRate optionally injects transient failures (0..1) to exercise the
	// client retry logic / timeouts. Set via NewWithFailRate.
	failRate float64
}

func New(s *store.Store) *Service {
	return &Service{store: s, idem: make(map[string]string)}
}

func NewWithFailRate(s *store.Store, failRate float64) *Service {
	svc := New(s)
	svc.failRate = failRate
	return svc
}

type CreateParams struct {
	OwnerID        string
	ContentType    string
	SizeBytes      int64
	Checksum       string
	IdempotencyKey string
}

// Create reserves a pending metadata record. Idempotent on IdempotencyKey.
func (svc *Service) Create(ctx context.Context, p CreateParams) (*store.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.OwnerID == "" || p.ContentType == "" || p.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: owner_id, content_type and idempotency_key are required", ErrInvalidArgument)
	}
	if err := svc.maybeFail(); err != nil {
		return nil, err
	}

	// Idempotency: same key -> original record, no duplicate.
	svc.idemMu.Lock()
	if existingID, ok := svc.idem[p.IdempotencyKey]; ok {
		svc.idemMu.Unlock()
		if img, found := svc.store.Get(existingID); found {
			return img, nil
		}
	} else {
		svc.idemMu.Unlock()
	}

	id := newID()
	img := &store.Image{
		ID:              id,
		OwnerID:         p.OwnerID,
		Bucket:          "img-prod",
		ObjectKey:       objectKey(id),
		ContentType:     p.ContentType,
		SizeBytes:       p.SizeBytes,
		Checksum:        p.Checksum,
		Status:          store.StatusPending,
		CreatedAtUnixMs: time.Now().UnixMilli(),
	}
	svc.store.Put(img)

	svc.idemMu.Lock()
	svc.idem[p.IdempotencyKey] = id
	svc.idemMu.Unlock()

	return img, nil
}

// Get returns one image by id.
func (svc *Service) Get(ctx context.Context, id string) (*store.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("%w: id is required", ErrInvalidArgument)
	}
	if err := svc.maybeFail(); err != nil {
		return nil, err
	}
	img, ok := svc.store.Get(id)
	if !ok || img.Status == store.StatusDeleted {
		return nil, ErrNotFound
	}
	return img, nil
}

type ListParams struct {
	OwnerID string
	Cursor  string
	Limit   int
}

type ListResult struct {
	Images     []*store.Image
	NextCursor string
}

// List returns an owner's images, newest first, via keyset cursor pagination.
func (svc *Service) List(ctx context.Context, p ListParams) (*ListResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.OwnerID == "" {
		return nil, fmt.Errorf("%w: owner_id is required", ErrInvalidArgument)
	}
	if err := svc.maybeFail(); err != nil {
		return nil, err
	}
	limit := p.Limit
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	after, err := decodeCursor(p.Cursor)
	if err != nil {
		return nil, fmt.Errorf("%w: bad cursor", ErrInvalidArgument)
	}
	// Fetch limit+1 to know whether there is a next page.
	imgs := svc.store.ListByOwner(p.OwnerID, after, limit+1)
	next := ""
	if len(imgs) > limit {
		next = encodeCursor(imgs[limit-1].ID)
		imgs = imgs[:limit]
	}
	return &ListResult{Images: imgs, NextCursor: next}, nil
}

// Delete soft-deletes an image.
func (svc *Service) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidArgument)
	}
	if err := svc.maybeFail(); err != nil {
		return err
	}
	if !svc.store.SetStatus(id, store.StatusDeleted) {
		return ErrNotFound
	}
	return nil
}

// maybeFail injects a transient "unavailable" error with probability failRate.
func (svc *Service) maybeFail() error {
	if svc.failRate <= 0 {
		return nil
	}
	var b [1]byte
	_, _ = rand.Read(b[:])
	if float64(b[0])/255.0 < svc.failRate {
		return fmt.Errorf("transient failure (injected)")
	}
	return nil
}

// ---- helpers ----

// newID returns a time-sortable id: 48-bit ms timestamp + 80 random bits,
// base32-hex encoded so lexical order == chronological order (ULID-like).
func newID() string {
	var buf [16]byte
	ms := uint64(time.Now().UnixMilli())
	buf[0] = byte(ms >> 40)
	buf[1] = byte(ms >> 32)
	buf[2] = byte(ms >> 24)
	buf[3] = byte(ms >> 16)
	buf[4] = byte(ms >> 8)
	buf[5] = byte(ms)
	_, _ = rand.Read(buf[6:])
	return base32hex.EncodeToString(buf[:])
}

func objectKey(id string) string {
	// shard prefix to avoid object-store hot partitions
	if len(id) >= 4 {
		return id[0:2] + "/" + id[2:4] + "/" + id
	}
	return id
}

func encodeCursor(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}

func decodeCursor(c string) (string, error) {
	if c == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// base32hex is Crockford-ish base32 with lexical ordering preserved.
var base32hex = newB32()

func newB32() *b32 { return &b32{} }

type b32 struct{}

func (e *b32) EncodeToString(src []byte) string {
	const alpha = "0123456789ABCDEFGHIJKLMNOPQRSTUV"
	// pad to multiple of 5 bytes worth of bits handled generically
	var out []byte
	var buf uint64
	var bits int
	for _, x := range src {
		buf = (buf << 8) | uint64(x)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out = append(out, alpha[(buf>>uint(bits))&0x1f])
		}
	}
	if bits > 0 {
		out = append(out, alpha[(buf<<uint(5-bits))&0x1f])
	}
	return string(out)
}
