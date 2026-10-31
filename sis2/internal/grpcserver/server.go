// Package grpcserver adapts the protocol-agnostic service to gRPC.
package grpcserver

import (
	"context"
	"errors"

	imagev1 "github.com/ZSultanbek/highload-back/sis2/gen/image/v1"
	"github.com/ZSultanbek/highload-back/sis2/internal/service"
	"github.com/ZSultanbek/highload-back/sis2/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements imagev1.ImageServiceServer.
type Server struct {
	imagev1.UnimplementedImageServiceServer
	svc *service.Service
}

func New(svc *service.Service) *Server { return &Server{svc: svc} }

func (s *Server) CreateImage(ctx context.Context, req *imagev1.CreateImageRequest) (*imagev1.Image, error) {
	img, err := s.svc.Create(ctx, service.CreateParams{
		OwnerID:        req.GetOwnerId(),
		ContentType:    req.GetContentType(),
		SizeBytes:      req.GetSizeBytes(),
		Checksum:       req.GetChecksum(),
		IdempotencyKey: req.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return toProto(img), nil
}

func (s *Server) GetImage(ctx context.Context, req *imagev1.GetImageRequest) (*imagev1.Image, error) {
	img, err := s.svc.Get(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return toProto(img), nil
}

func (s *Server) ListImages(ctx context.Context, req *imagev1.ListImagesRequest) (*imagev1.ListImagesResponse, error) {
	res, err := s.svc.List(ctx, service.ListParams{
		OwnerID: req.GetOwnerId(),
		Cursor:  req.GetCursor(),
		Limit:   int(req.GetLimit()),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	out := &imagev1.ListImagesResponse{NextCursor: res.NextCursor}
	for _, img := range res.Images {
		out.Images = append(out.Images, toProto(img))
	}
	return out, nil
}

func (s *Server) DeleteImage(ctx context.Context, req *imagev1.DeleteImageRequest) (*imagev1.DeleteImageResponse, error) {
	if err := s.svc.Delete(ctx, req.GetId()); err != nil {
		return nil, toStatus(err)
	}
	return &imagev1.DeleteImageResponse{}, nil
}

func toProto(img *store.Image) *imagev1.Image {
	return &imagev1.Image{
		Id:              img.ID,
		OwnerId:         img.OwnerID,
		Bucket:          img.Bucket,
		ObjectKey:       img.ObjectKey,
		ContentType:     img.ContentType,
		SizeBytes:       img.SizeBytes,
		Width:           img.Width,
		Height:          img.Height,
		Checksum:        img.Checksum,
		Status:          imagev1.Status(img.Status),
		CreatedAtUnixMs: img.CreatedAtUnixMs,
	}
}

// toStatus maps domain errors to gRPC status codes. Transient/injected
// failures become UNAVAILABLE so the client's retry policy kicks in.
func toStatus(err error) error {
	switch {
	case errors.Is(err, service.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, service.ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	default:
		return status.Error(codes.Unavailable, err.Error())
	}
}
