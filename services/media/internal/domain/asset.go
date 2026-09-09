// Package domain holds the media service's rules: which buckets and content
// types are allowed, size limits, and how a safe object key is derived from an
// uploaded filename.
package domain

import (
	"crypto/rand"
	"encoding/hex"
	"path"
	"strings"
	"time"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

type Status string

const (
	StatusPending  Status = "PENDING"
	StatusReady    Status = "READY"
	StatusRejected Status = "REJECTED"
)

// MaxUploadBytes is the hard cap enforced when issuing an upload URL.
const MaxUploadBytes = 25 << 20 // 25 MiB

var allowedBuckets = map[string]bool{
	"product-media": true,
	"invoices":      true,
	"exports":       true,
}

var allowedContentTypes = map[string]string{
	"image/jpeg":       ".jpg",
	"image/png":        ".png",
	"image/webp":       ".webp",
	"image/avif":       ".avif",
	"application/pdf":  ".pdf",
	"text/csv":         ".csv",
	"application/json": ".json",
}

// Asset is the tracked object.
type Asset struct {
	Key         string
	Bucket      string
	ContentType string
	SizeBytes   int64
	Status      Status
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewUpload validates an upload request and returns a PENDING asset with a
// freshly minted, collision-resistant key: "<bucket>/<yyyy>/<mm>/<rand>.<ext>".
func NewUpload(bucket, filename, contentType string, sizeBytes int64, createdBy string) (*Asset, error) {
	if !allowedBuckets[bucket] {
		return nil, errs.New(errs.KindInvalidArgument, "BUCKET_NOT_ALLOWED", "unknown bucket")
	}
	ext, ok := allowedContentTypes[contentType]
	if !ok {
		return nil, errs.New(errs.KindInvalidArgument, "CONTENT_TYPE_NOT_ALLOWED", "unsupported content_type")
	}
	if sizeBytes <= 0 || sizeBytes > MaxUploadBytes {
		return nil, errs.New(errs.KindInvalidArgument, "SIZE_OUT_OF_RANGE", "size_bytes must be 1..25MiB")
	}
	// Prefer the real extension if the filename carries a matching one.
	if fe := strings.ToLower(path.Ext(filename)); fe != "" && isKnownExt(fe) {
		ext = fe
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	now := time.Now().UTC()
	key := bucket + "/" + now.Format("2006/01") + "/" + hex.EncodeToString(b[:]) + ext

	return &Asset{
		Key: key, Bucket: bucket, ContentType: contentType,
		SizeBytes: sizeBytes, Status: StatusPending, CreatedBy: createdBy,
	}, nil
}

// MarkReady transitions PENDING -> READY after the object is confirmed present.
func (a *Asset) MarkReady(actualSize int64, actualContentType string) error {
	if a.Status == StatusReady {
		return nil // idempotent
	}
	if a.Status != StatusPending {
		return errs.New(errs.KindFailedPrecondition, "NOT_PENDING", "asset is not awaiting confirmation")
	}
	if actualSize <= 0 || actualSize > MaxUploadBytes {
		return errs.New(errs.KindFailedPrecondition, "BAD_OBJECT_SIZE", "uploaded object size is invalid")
	}
	if actualContentType != "" && actualContentType != a.ContentType {
		return errs.New(errs.KindFailedPrecondition, "CONTENT_TYPE_MISMATCH", "uploaded content type differs from the declared one")
	}
	a.SizeBytes = actualSize
	a.Status = StatusReady
	return nil
}

func isKnownExt(ext string) bool {
	for _, e := range allowedContentTypes {
		if e == ext {
			return true
		}
	}
	return false
}
