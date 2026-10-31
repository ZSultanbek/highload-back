// Package restserver is the REST/JSON baseline (HTTP/1.1) exposing the same
// operations as the gRPC server, backed by the same service logic.
package restserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/ZSultanbek/highload-back/sis2/internal/service"
	"github.com/ZSultanbek/highload-back/sis2/internal/store"
)

type Server struct {
	svc *service.Service
	mux *http.ServeMux
}

func New(svc *service.Service) *Server {
	s := &Server{svc: svc, mux: http.NewServeMux()}
	// Go 1.22+ method+pattern routing.
	s.mux.HandleFunc("POST /v1/images", s.create)
	s.mux.HandleFunc("GET /v1/images/{id}", s.get)
	s.mux.HandleFunc("GET /v1/images", s.list)
	s.mux.HandleFunc("DELETE /v1/images/{id}", s.delete)
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

// JSON DTOs — the baseline encoding we measure protobuf against.
type imageJSON struct {
	ID              string `json:"id"`
	OwnerID         string `json:"owner_id"`
	Bucket          string `json:"bucket"`
	ObjectKey       string `json:"object_key"`
	ContentType     string `json:"content_type"`
	SizeBytes       int64  `json:"size_bytes"`
	Width           int32  `json:"width"`
	Height          int32  `json:"height"`
	Checksum        string `json:"checksum"`
	Status          string `json:"status"`
	CreatedAtUnixMs int64  `json:"created_at_unix_ms"`
}

type createReq struct {
	OwnerID        string `json:"owner_id"`
	ContentType    string `json:"content_type"`
	SizeBytes      int64  `json:"size_bytes"`
	Checksum       string `json:"checksum"`
	IdempotencyKey string `json:"idempotency_key"`
}

type listResp struct {
	Images     []imageJSON `json:"images"`
	NextCursor string      `json:"next_cursor"`
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	img, err := s.svc.Create(r.Context(), service.CreateParams{
		OwnerID:        req.OwnerID,
		ContentType:    req.ContentType,
		SizeBytes:      req.SizeBytes,
		Checksum:       req.Checksum,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toJSON(img))
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	img, err := s.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toJSON(img))
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	res, err := s.svc.List(r.Context(), service.ListParams{
		OwnerID: r.URL.Query().Get("owner_id"),
		Cursor:  r.URL.Query().Get("cursor"),
		Limit:   limit,
	})
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	out := listResp{NextCursor: res.NextCursor}
	for _, img := range res.Images {
		out.Images = append(out.Images, toJSON(img))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeDomainErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toJSON(img *store.Image) imageJSON {
	return imageJSON{
		ID:              img.ID,
		OwnerID:         img.OwnerID,
		Bucket:          img.Bucket,
		ObjectKey:       img.ObjectKey,
		ContentType:     img.ContentType,
		SizeBytes:       img.SizeBytes,
		Width:           img.Width,
		Height:          img.Height,
		Checksum:        img.Checksum,
		Status:          statusString(img.Status),
		CreatedAtUnixMs: img.CreatedAtUnixMs,
	}
}

func statusString(st store.Status) string {
	switch st {
	case store.StatusPending:
		return "PENDING"
	case store.StatusReady:
		return "READY"
	case store.StatusDeleted:
		return "DELETED"
	default:
		return "UNSPECIFIED"
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// writeDomainErr maps service errors to HTTP status codes. Transient failures
// become 503 so a REST client can apply the same retry policy as gRPC.
func writeDomainErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, service.ErrInvalidArgument):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	}
}
