// Package objstore wraps the S3-compatible object store (MinIO). It keeps two
// clients: one bound to the PUBLIC endpoint used to sign upload URLs (the host
// the uploader reaches — a CDN/edge in prod, localhost:9000 locally), and one
// bound to the INTERNAL endpoint for server-side ops like HEAD.
package objstore

import (
	"context"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

type Config struct {
	// InternalEndpoint: host:port the media service itself reaches (e.g. minio:9000).
	InternalEndpoint string
	// PublicEndpoint: host:port the uploader reaches; upload URLs are signed for it.
	PublicEndpoint string
	AccessKey      string
	SecretKey      string
	UseSSL         bool
	Region         string // defaults to us-east-1 (MinIO's default)
	// PublicBaseURL is prepended to a key to form the canonical served URL.
	PublicBaseURL string
}

type Store struct {
	internal *minio.Client
	public   *minio.Client
	baseURL  string
}

func New(cfg Config) (*Store, error) {
	region := cfg.Region
	if region == "" {
		region = "us-east-1" // MinIO default; setting it lets PresignedPutObject
	} // skip the online bucket-location lookup.
	newOpts := func() *minio.Options {
		return &minio.Options{
			Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
			Secure: cfg.UseSSL,
			Region: region,
		}
	}
	internal, err := minio.New(cfg.InternalEndpoint, newOpts())
	if err != nil {
		return nil, errs.Wrap(err, errs.KindInternal, "OBJSTORE_INIT", "cannot init internal object store client")
	}
	public := internal
	if cfg.PublicEndpoint != "" && cfg.PublicEndpoint != cfg.InternalEndpoint {
		public, err = minio.New(cfg.PublicEndpoint, newOpts())
		if err != nil {
			return nil, errs.Wrap(err, errs.KindInternal, "OBJSTORE_INIT", "cannot init public object store client")
		}
	}
	return &Store{internal: internal, public: public, baseURL: cfg.PublicBaseURL}, nil
}

// PresignPut returns a presigned PUT URL for bucket/key, signed for the PUBLIC endpoint.
func (s *Store) PresignPut(ctx context.Context, bucket, key string, ttl time.Duration) (string, time.Time, error) {
	u, err := s.public.PresignedPutObject(ctx, bucket, keyWithinBucket(bucket, key), ttl)
	if err != nil {
		return "", time.Time{}, errs.Wrap(err, errs.KindInternal, "PRESIGN_FAILED", "cannot presign upload: "+err.Error())
	}
	return u.String(), time.Now().Add(ttl), nil
}

// Head returns (size, contentType, found) via the INTERNAL client.
func (s *Store) Head(ctx context.Context, bucket, key string) (int64, string, bool, error) {
	info, err := s.internal.StatObject(ctx, bucket, keyWithinBucket(bucket, key), minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return 0, "", false, nil
		}
		return 0, "", false, errs.Wrap(err, errs.KindInternal, "HEAD_FAILED", "cannot stat object: "+err.Error())
	}
	return info.Size, info.ContentType, true, nil
}

// PublicURL returns the canonical served URL for a full key ("<bucket>/<path>").
func (s *Store) PublicURL(fullKey string) string {
	if s.baseURL == "" {
		return "/" + fullKey
	}
	return s.baseURL + "/" + fullKey
}

func keyWithinBucket(bucket, fullKey string) string {
	prefix := bucket + "/"
	if len(fullKey) > len(prefix) && fullKey[:len(prefix)] == prefix {
		return fullKey[len(prefix):]
	}
	return fullKey
}
