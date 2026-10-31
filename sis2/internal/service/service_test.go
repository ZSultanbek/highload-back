package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ZSultanbek/highload-back/sis2/internal/store"
)

func newSvc() *Service { return New(store.New()) }

func TestCreateIsIdempotent(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	p := CreateParams{OwnerID: "o1", ContentType: "image/jpeg", SizeBytes: 100, IdempotencyKey: "k1"}

	a, err := svc.Create(ctx, p)
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := svc.Create(ctx, p) // same key
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("idempotency broken: %s != %s", a.ID, b.ID)
	}

	// different key -> different record
	p2 := p
	p2.IdempotencyKey = "k2"
	c, _ := svc.Create(ctx, p2)
	if c.ID == a.ID {
		t.Fatalf("different key should produce a new id")
	}
}

func TestCreateValidation(t *testing.T) {
	svc := newSvc()
	_, err := svc.Create(context.Background(), CreateParams{OwnerID: "", ContentType: "x", IdempotencyKey: "k"})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestGetNotFound(t *testing.T) {
	svc := newSvc()
	_, err := svc.Get(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteThenGet(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	img, _ := svc.Create(ctx, CreateParams{OwnerID: "o1", ContentType: "image/png", IdempotencyKey: "k"})
	if err := svc.Delete(ctx, img.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.Get(ctx, img.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted image should be not found, got %v", err)
	}
}

func TestListPaginationNewestFirst(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	var ids []string
	for i := 0; i < 5; i++ {
		img, _ := svc.Create(ctx, CreateParams{
			OwnerID:        "owner",
			ContentType:    "image/jpeg",
			IdempotencyKey: string(rune('a' + i)),
		})
		ids = append(ids, img.ID)
	}

	// first page
	page1, err := svc.List(ctx, ListParams{OwnerID: "owner", Limit: 2})
	if err != nil {
		t.Fatalf("list p1: %v", err)
	}
	if len(page1.Images) != 2 {
		t.Fatalf("want 2 images, got %d", len(page1.Images))
	}
	if page1.NextCursor == "" {
		t.Fatalf("expected a next cursor")
	}
	// newest first: time-sortable ids descending
	if page1.Images[0].ID < page1.Images[1].ID {
		t.Fatalf("not newest-first")
	}

	// walk all pages; expect 5 unique, no overlaps
	seen := map[string]bool{}
	cursor := ""
	for {
		pg, err := svc.List(ctx, ListParams{OwnerID: "owner", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("list page: %v", err)
		}
		for _, img := range pg.Images {
			if seen[img.ID] {
				t.Fatalf("duplicate across pages: %s", img.ID)
			}
			seen[img.ID] = true
		}
		if pg.NextCursor == "" {
			break
		}
		cursor = pg.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("expected 5 images across pages, got %d", len(seen))
	}
}

func TestIDsAreTimeSortable(t *testing.T) {
	var prev string
	for i := 0; i < 50; i++ {
		id := newID()
		if id <= prev {
			// allow equal-ms randomness ordering by requiring only non-decreasing ms prefix
			if id[:9] < prev[:9] {
				t.Fatalf("ids not time-sortable: %s then %s", prev, id)
			}
		}
		prev = id
	}
}
