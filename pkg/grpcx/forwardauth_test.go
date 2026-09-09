package grpcx

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestForwardAuthInterceptor(t *testing.T) {
	capture := func(ctx context.Context) []string {
		md, _ := metadata.FromOutgoingContext(ctx)
		return md.Get("authorization")
	}

	// Incoming bearer -> copied onto the outgoing context.
	in := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer tok-123"))
	var got []string
	err := forwardAuthInterceptor(in, "/svc/M", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			got = capture(ctx)
			return nil
		})
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if len(got) != 1 || got[0] != "Bearer tok-123" {
		t.Fatalf("outgoing authorization = %v, want [Bearer tok-123]", got)
	}

	// No incoming metadata -> nothing added, call still proceeds.
	got = nil
	err = forwardAuthInterceptor(context.Background(), "/svc/M", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			got = capture(ctx)
			return nil
		})
	if err != nil || len(got) != 0 {
		t.Fatalf("no-metadata case: err=%v got=%v", err, got)
	}
}
