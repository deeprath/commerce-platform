// Package grpcsvc adapts the MediaService contract to the domain, store and
// object store. Writes require the catalog_manager role.
package grpcsvc

import (
	"context"
	"time"

	mediav1 "github.com/deeprath/commerce-platform/gen/go/commerce/media/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/services/media/internal/domain"
	"github.com/deeprath/commerce-platform/services/media/internal/store"
)

const roleCatalogManager = "catalog_manager"
const uploadTTL = 15 * time.Minute

// objectStore is the slice of *objstore.Store the handlers below actually
// call. Narrowing to an interface here (rather than depending on the
// concrete MinIO-backed client) lets tests fake the object store without
// standing up a real MinIO for what are otherwise pure request-mapping unit
// tests — the same reasoning as services/search's "searcher" interface.
type objectStore interface {
	PresignPut(ctx context.Context, bucket, key string, ttl time.Duration) (string, time.Time, error)
	Head(ctx context.Context, bucket, key string) (int64, string, bool, error)
	PublicURL(fullKey string) string
}

type Server struct {
	mediav1.UnimplementedMediaServiceServer
	store *store.Store
	obj   objectStore
}

func New(s *store.Store, o objectStore) *Server { return &Server{store: s, obj: o} }

func (s *Server) CreateUploadURL(ctx context.Context, req *mediav1.CreateUploadURLRequest) (*mediav1.CreateUploadURLResponse, error) {
	if err := grpcx.RequireRole(ctx, roleCatalogManager); err != nil {
		return nil, err
	}
	sub := ""
	if p := auth.FromContext(ctx); p != nil {
		sub = p.Subject
	}
	a, err := domain.NewUpload(req.GetBucket(), req.GetFilename(), req.GetContentType(), req.GetSizeBytes(), sub)
	if err != nil {
		return nil, err
	}
	if err := s.store.Insert(ctx, a); err != nil {
		return nil, err
	}
	url, expires, err := s.obj.PresignPut(ctx, a.Bucket, a.Key, uploadTTL)
	if err != nil {
		return nil, err
	}
	return &mediav1.CreateUploadURLResponse{
		Key: a.Key, UploadUrl: url, ExpiresAt: expires.UTC().Format(time.RFC3339),
	}, nil
}

func (s *Server) ConfirmUpload(ctx context.Context, req *mediav1.ConfirmUploadRequest) (*mediav1.ConfirmUploadResponse, error) {
	if err := grpcx.RequireRole(ctx, roleCatalogManager); err != nil {
		return nil, err
	}
	a, err := s.store.Get(ctx, req.GetKey())
	if err != nil {
		return nil, err
	}
	size, ctype, found, err := s.obj.Head(ctx, a.Bucket, a.Key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errs.New(errs.KindFailedPrecondition, "OBJECT_MISSING", "no object was uploaded for this key")
	}
	if err := a.MarkReady(size, ctype); err != nil {
		return nil, err
	}
	servedURL := s.obj.PublicURL(a.Key)
	if err := s.store.MarkReady(ctx, a, servedURL); err != nil {
		return nil, err
	}
	return &mediav1.ConfirmUploadResponse{Asset: toAsset(a, servedURL)}, nil
}

func (s *Server) GetAsset(ctx context.Context, req *mediav1.GetAssetRequest) (*mediav1.GetAssetResponse, error) {
	a, err := s.store.Get(ctx, req.GetKey())
	if err != nil {
		return nil, err
	}
	return toAsset(a, s.obj.PublicURL(a.Key)), nil
}

func toAsset(a *domain.Asset, url string) *mediav1.GetAssetResponse {
	return &mediav1.GetAssetResponse{
		Key:         a.Key,
		Url:         url,
		Status:      protoStatus(a.Status),
		ContentType: a.ContentType,
		SizeBytes:   a.SizeBytes,
	}
}

func protoStatus(s domain.Status) mediav1.AssetStatus {
	switch s {
	case domain.StatusPending:
		return mediav1.AssetStatus_ASSET_STATUS_PENDING
	case domain.StatusReady:
		return mediav1.AssetStatus_ASSET_STATUS_READY
	case domain.StatusRejected:
		return mediav1.AssetStatus_ASSET_STATUS_REJECTED
	default:
		return mediav1.AssetStatus_ASSET_STATUS_UNSPECIFIED
	}
}
