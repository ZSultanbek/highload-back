// Package store is an in-memory, concurrency-safe metadata store.
//
// SIS #2 is about the PROTOCOL, not persistence, so there is no real Postgres
// or S3 here. Both the gRPC and REST servers share one *Store instance, which
// keeps the latency/payload comparison fair: the only thing that differs
// between the two servers is the wire protocol, never the business logic.
package store

import (
	"sort"
	"sync"
)

// Status mirrors the two-phase upload lifecycle.
type Status int32

const (
	StatusUnspecified Status = 0
	StatusPending     Status = 1
	StatusReady       Status = 2
	StatusDeleted     Status = 3
)

// Image is the domain metadata record (protocol-agnostic).
type Image struct {
	ID              string
	OwnerID         string
	Bucket          string
	ObjectKey       string
	ContentType     string
	SizeBytes       int64
	Width           int32
	Height          int32
	Checksum        string
	Status          Status
	CreatedAtUnixMs int64
}

// Store is a thread-safe in-memory image store.
type Store struct {
	mu      sync.RWMutex
	byID    map[string]*Image
	byOwner map[string][]string // owner_id -> image ids (insertion order)
}

func New() *Store {
	return &Store{
		byID:    make(map[string]*Image),
		byOwner: make(map[string][]string),
	}
}

// Put inserts or replaces an image.
func (s *Store) Put(img *Image) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byID[img.ID]; !exists {
		s.byOwner[img.OwnerID] = append(s.byOwner[img.OwnerID], img.ID)
	}
	cp := *img
	s.byID[img.ID] = &cp
}

// Get returns a copy of the image and whether it was found.
func (s *Store) Get(id string) (*Image, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	img, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	cp := *img
	return &cp, true
}

// SetStatus updates an image's status. Returns false if not found.
func (s *Store) SetStatus(id string, st Status) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	img, ok := s.byID[id]
	if !ok {
		return false
	}
	img.Status = st
	return true
}

// ListByOwner returns up to limit images for an owner, newest first, starting
// strictly after afterID (keyset pagination). afterID == "" starts at the top.
func (s *Store) ListByOwner(ownerID, afterID string, limit int) []*Image {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := s.byOwner[ownerID]
	imgs := make([]*Image, 0, len(ids))
	for _, id := range ids {
		if img, ok := s.byID[id]; ok && img.Status != StatusDeleted {
			imgs = append(imgs, img)
		}
	}
	// Newest first: ids are time-sortable, so sort by ID descending.
	sort.Slice(imgs, func(i, j int) bool { return imgs[i].ID > imgs[j].ID })

	start := 0
	if afterID != "" {
		for i, img := range imgs {
			if img.ID == afterID {
				start = i + 1
				break
			}
		}
	}
	if start >= len(imgs) {
		return nil
	}
	end := start + limit
	if end > len(imgs) {
		end = len(imgs)
	}
	out := make([]*Image, 0, end-start)
	for _, img := range imgs[start:end] {
		cp := *img
		out = append(out, &cp)
	}
	return out
}
