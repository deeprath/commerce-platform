package grpcsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	mediav1 "github.com/deeprath/commerce-platform/gen/go/commerce/media/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/media/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/media/internal/store"
)

// --- a real Postgres for the store, a fake for the object store ----------

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("media"),
		tcpostgres.WithUsername("t"), tcpostgres.WithPassword("t"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if err := pgx.Migrate(ctx, dsn, store.Migrations, "migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgx.NewPool(ctx, pgx.PoolConfig{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// fakeObjStore stands in for the MinIO-backed objstore.Store. presignErr/
// headErr/headFound/headSize/headContentType let each test drive exactly the
// object-store outcome it needs without a real MinIO.
type fakeObjStore struct {
	presignErr                        error
	headErr                           error
	headFound                         bool
	headSize                          int64
	headContentType                   string
	lastPresignBucket, lastPresignKey string
	lastHeadBucket, lastHeadKey       string
}

func (f *fakeObjStore) PresignPut(_ context.Context, bucket, key string, ttl time.Duration) (string, time.Time, error) {
	f.lastPresignBucket, f.lastPresignKey = bucket, key
	if f.presignErr != nil {
		return "", time.Time{}, f.presignErr
	}
	return "https://upload.example/" + key, time.Now().Add(ttl), nil
}

func (f *fakeObjStore) Head(_ context.Context, bucket, key string) (int64, string, bool, error) {
	f.lastHeadBucket, f.lastHeadKey = bucket, key
	if f.headErr != nil {
		return 0, "", false, f.headErr
	}
	return f.headSize, f.headContentType, f.headFound, nil
}

func (f *fakeObjStore) PublicURL(fullKey string) string { return "https://cdn.example/" + fullKey }

func newSrv(t *testing.T) (*grpcsvc.Server, *fakeObjStore) {
	obj := &fakeObjStore{headFound: true, headSize: 1024, headContentType: "image/jpeg"}
	return grpcsvc.New(store.New(spinUp(t)), obj), obj
}

func asManager(ctx context.Context) context.Context {
	return auth.WithPrincipal(ctx, &auth.Principal{Subject: "mgr-1", Roles: []string{"catalog_manager"}})
}

// --- CreateUploadURL -------------------------------------------------------

func TestCreateUploadURL_RequiresCatalogManagerRole(t *testing.T) {
	s, _ := newSrv(t)
	req := &mediav1.CreateUploadURLRequest{Bucket: "product-media", Filename: "a.jpg", ContentType: "image/jpeg", SizeBytes: 100}

	if _, err := s.CreateUploadURL(context.Background(), req); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("no principal: err = %v, want KindUnauthenticated", err)
	}
	noRole := auth.WithPrincipal(context.Background(), &auth.Principal{Subject: "u1", Roles: []string{"customer"}})
	if _, err := s.CreateUploadURL(noRole, req); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("wrong role: err = %v, want KindPermissionDenied", err)
	}
}

func TestCreateUploadURL_RejectsAnUnknownBucketOrContentType(t *testing.T) {
	s, _ := newSrv(t)
	ctx := asManager(context.Background())

	if _, err := s.CreateUploadURL(ctx, &mediav1.CreateUploadURLRequest{
		Bucket: "not-a-real-bucket", Filename: "a.jpg", ContentType: "image/jpeg", SizeBytes: 100,
	}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("unknown bucket: err = %v", err)
	}
	if _, err := s.CreateUploadURL(ctx, &mediav1.CreateUploadURLRequest{
		Bucket: "product-media", Filename: "a.exe", ContentType: "application/x-msdownload", SizeBytes: 100,
	}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("disallowed content type: err = %v", err)
	}
}

func TestCreateUploadURL_HappyPathPersistsAndPresigns(t *testing.T) {
	s, obj := newSrv(t)
	ctx := asManager(context.Background())

	resp, err := s.CreateUploadURL(ctx, &mediav1.CreateUploadURLRequest{
		Bucket: "product-media", Filename: "hero.jpg", ContentType: "image/jpeg", SizeBytes: 2048,
	})
	if err != nil {
		t.Fatalf("CreateUploadURL: %v", err)
	}
	if resp.GetKey() == "" || resp.GetUploadUrl() == "" || resp.GetExpiresAt() == "" {
		t.Fatalf("resp = %+v", resp)
	}
	if obj.lastPresignBucket != "product-media" || obj.lastPresignKey != resp.GetKey() {
		t.Fatalf("PresignPut called with bucket=%q key=%q, want product-media/%s", obj.lastPresignBucket, obj.lastPresignKey, resp.GetKey())
	}
	if _, err := time.Parse(time.RFC3339, resp.GetExpiresAt()); err != nil {
		t.Fatalf("expires_at not RFC3339: %v", err)
	}

	// The asset landed in the store as PENDING and is fetchable.
	got, err := s.GetAsset(context.Background(), &mediav1.GetAssetRequest{Key: resp.GetKey()})
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if got.GetStatus() != mediav1.AssetStatus_ASSET_STATUS_PENDING {
		t.Fatalf("status = %v, want PENDING", got.GetStatus())
	}
}

func TestCreateUploadURL_PresignFailurePropagates(t *testing.T) {
	s, obj := newSrv(t)
	obj.presignErr = errs.New(errs.KindUnavailable, "OBJSTORE_DOWN", "minio unreachable")
	ctx := asManager(context.Background())

	_, err := s.CreateUploadURL(ctx, &mediav1.CreateUploadURLRequest{
		Bucket: "product-media", Filename: "a.jpg", ContentType: "image/jpeg", SizeBytes: 100,
	})
	if !errs.Is(err, errs.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
}

// --- ConfirmUpload ---------------------------------------------------------

func createPending(t *testing.T, s *grpcsvc.Server) string {
	t.Helper()
	resp, err := s.CreateUploadURL(asManager(context.Background()), &mediav1.CreateUploadURLRequest{
		Bucket: "product-media", Filename: "hero.jpg", ContentType: "image/jpeg", SizeBytes: 2048,
	})
	if err != nil {
		t.Fatalf("CreateUploadURL (fixture): %v", err)
	}
	return resp.GetKey()
}

func TestConfirmUpload_RequiresCatalogManagerRole(t *testing.T) {
	s, _ := newSrv(t)
	key := createPending(t, s)
	if _, err := s.ConfirmUpload(context.Background(), &mediav1.ConfirmUploadRequest{Key: key}); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("err = %v, want KindUnauthenticated", err)
	}
}

func TestConfirmUpload_MissingObjectIsFailedPrecondition(t *testing.T) {
	s, obj := newSrv(t)
	key := createPending(t, s)
	obj.headFound = false

	_, err := s.ConfirmUpload(asManager(context.Background()), &mediav1.ConfirmUploadRequest{Key: key})
	if !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("err = %v, want KindFailedPrecondition", err)
	}
}

func TestConfirmUpload_ContentTypeMismatchRejected(t *testing.T) {
	s, obj := newSrv(t)
	key := createPending(t, s)
	obj.headContentType = "application/pdf" // asset was declared image/jpeg

	_, err := s.ConfirmUpload(asManager(context.Background()), &mediav1.ConfirmUploadRequest{Key: key})
	if !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("err = %v, want KindFailedPrecondition", err)
	}
}

func TestConfirmUpload_HappyPathMarksReadyAndReturnsAPublicURL(t *testing.T) {
	s, obj := newSrv(t)
	key := createPending(t, s)
	obj.headSize = 4096

	resp, err := s.ConfirmUpload(asManager(context.Background()), &mediav1.ConfirmUploadRequest{Key: key})
	if err != nil {
		t.Fatalf("ConfirmUpload: %v", err)
	}
	a := resp.GetAsset()
	if a.GetStatus() != mediav1.AssetStatus_ASSET_STATUS_READY {
		t.Fatalf("status = %v, want READY", a.GetStatus())
	}
	if a.GetSizeBytes() != 4096 {
		t.Fatalf("size_bytes = %d, want 4096 (from Head, not the declared size)", a.GetSizeBytes())
	}
	if a.GetUrl() != "https://cdn.example/"+key {
		t.Fatalf("url = %q", a.GetUrl())
	}

	// Persisted, not just returned: a fresh GetAsset agrees.
	got, err := s.GetAsset(context.Background(), &mediav1.GetAssetRequest{Key: key})
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if got.GetStatus() != mediav1.AssetStatus_ASSET_STATUS_READY {
		t.Fatalf("re-fetched status = %v, want READY", got.GetStatus())
	}
}

func TestConfirmUpload_UnknownKeyIsNotFound(t *testing.T) {
	s, _ := newSrv(t)
	_, err := s.ConfirmUpload(asManager(context.Background()), &mediav1.ConfirmUploadRequest{Key: "product-media/nope.jpg"})
	if !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("err = %v, want KindNotFound", err)
	}
}

// --- GetAsset ---------------------------------------------------------

func TestGetAsset_IsPublicNoRoleRequired(t *testing.T) {
	s, _ := newSrv(t)
	key := createPending(t, s)
	if _, err := s.GetAsset(context.Background(), &mediav1.GetAssetRequest{Key: key}); err != nil {
		t.Fatalf("GetAsset with no principal at all: %v", err)
	}
}

func TestGetAsset_UnknownKeyIsNotFound(t *testing.T) {
	s, _ := newSrv(t)
	_, err := s.GetAsset(context.Background(), &mediav1.GetAssetRequest{Key: "product-media/nope.jpg"})
	if !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("err = %v, want KindNotFound", err)
	}
}
