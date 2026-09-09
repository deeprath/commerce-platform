package grpcsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	notificationv1 "github.com/deeprath/commerce-platform/gen/go/commerce/notification/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/notification/internal/channel"
	"github.com/deeprath/commerce-platform/services/notification/internal/domain"
	"github.com/deeprath/commerce-platform/services/notification/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/notification/internal/store"
)

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("notification"),
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

func as(sub string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Subject: sub, Roles: []string{"customer"}})
}

func TestSendTestAndListOwnerScoped(t *testing.T) {
	st := store.New(spinUp(t))
	s := grpcsvc.New(st, channel.LogChannel{})

	// Needs a principal.
	if _, err := s.SendTest(context.Background(), &notificationv1.SendTestRequest{}); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("anon SendTest: want Unauthenticated, got %v", err)
	}
	if _, err := s.ListNotifications(context.Background(), &notificationv1.ListNotificationsRequest{}); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("anon List: want Unauthenticated, got %v", err)
	}

	sent, err := s.SendTest(as("me"), &notificationv1.SendTestRequest{To: "me@example.com"})
	if err != nil {
		t.Fatalf("SendTest: %v", err)
	}
	if sent.GetKind() != "test" || sent.GetOwnerId() != "me" ||
		sent.GetStatus() != notificationv1.DeliveryStatus_DELIVERY_STATUS_SENT ||
		sent.GetChannel() != notificationv1.Channel_CHANNEL_EMAIL || sent.GetSubject() == "" {
		t.Fatalf("bad test notification: %+v", sent)
	}

	// Seed one for another user; it must not show up in "me"'s list.
	if _, err := st.Record(context.Background(), domain.Notification{
		OwnerID: "other", Kind: "order_confirmed", Channel: domain.ChannelEmail, Status: domain.StatusSent,
	}, ""); err != nil {
		t.Fatalf("seed other: %v", err)
	}

	list, err := s.ListNotifications(as("me"), &notificationv1.ListNotificationsRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.GetNotifications()) != 1 || list.GetNotifications()[0].GetId() != sent.GetId() {
		t.Fatalf("list scoped wrong: %+v", list.GetNotifications())
	}
	if list.GetNotifications()[0].GetCreatedAt() == "" {
		t.Fatalf("created_at not mapped")
	}
}
