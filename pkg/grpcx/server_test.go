package grpcx

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
)

// --- a tiny real JWKS + Verifier, so authUnary is exercised with genuine
// token verification rather than a fake --------------------------------

const testKID = "grpcx-test-kid"

func jwksServer(t *testing.T, pub *rsa.PublicKey) string {
	t.Helper()
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	eBytes := big.NewInt(int64(pub.E)).Bytes()
	e := base64.RawURLEncoding.EncodeToString(eBytes)
	jwk := map[string]any{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": testKID, "n": n, "e": e}
	body, err := json.Marshal(map[string]any{"keys": []any{jwk}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func testVerifier(t *testing.T) (*auth.Verifier, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	url := jwksServer(t, &key.PublicKey)
	v, err := auth.NewVerifier(t.Context(), auth.Config{JWKSURL: url, Issuer: "https://issuer.test"})
	if err != nil {
		t.Fatalf("auth.NewVerifier: %v", err)
	}
	return v, key
}

func signToken(t *testing.T, key *rsa.PrivateKey, subject string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": subject, "iss": "https://issuer.test", "exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = testKID
	raw, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// --- a minimal real gRPC service (grpc.health.v1.Health) to drive the
// whole NewServer interceptor chain over a real (bufconn) network round
// trip, rather than calling an interceptor function in isolation --------

type stubHealthServer struct {
	grpc_health_v1.UnimplementedHealthServer
	panicOnCheck bool
}

func (s *stubHealthServer) Check(ctx context.Context, _ *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	if s.panicOnCheck {
		panic("boom")
	}
	st := grpc_health_v1.HealthCheckResponse_NOT_SERVING
	if auth.FromContext(ctx) != nil {
		st = grpc_health_v1.HealthCheckResponse_SERVING // proves the Principal was injected
	}
	return &grpc_health_v1.HealthCheckResponse{Status: st}, nil
}

// startServer builds a *grpc.Server via NewServer(opts...), serves the stub
// health service over an in-memory bufconn listener, and returns a connected
// client plus a cleanup func.
func startServer(t *testing.T, health *stubHealthServer, opts ...ServerOption) grpc_health_v1.HealthClient {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := NewServer(opts...)
	grpc_health_v1.RegisterHealthServer(srv, health)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return grpc_health_v1.NewHealthClient(conn)
}

func withBearer(ctx context.Context, raw string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+raw))
}

const checkMethod = "/grpc.health.v1.Health/Check"

func TestNewServer_NoAuthConfigured_CallsThroughUnauthenticated(t *testing.T) {
	client := startServer(t, &stubHealthServer{})
	resp, err := client.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status = %v, want NOT_SERVING (no auth wired at all => no principal)", resp.GetStatus())
	}
}

func TestNewServer_WithAuth_RequiredMethodRejectsNoToken(t *testing.T) {
	v, _ := testVerifier(t)
	client := startServer(t, &stubHealthServer{}, WithAuth(v))
	_, err := client.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("err = %v, want Unauthenticated", err)
	}
}

func TestNewServer_WithAuth_SkippedMethodBypassesEntirely(t *testing.T) {
	v, _ := testVerifier(t)
	client := startServer(t, &stubHealthServer{}, WithAuth(v, checkMethod))
	resp, err := client.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("skipped method should need no token: %v", err)
	}
	if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status = %v, want NOT_SERVING (skip never verifies, never injects)", resp.GetStatus())
	}
}

func TestNewServer_WithAuth_OptionalMethodAllowsAnonymous(t *testing.T) {
	v, _ := testVerifier(t)
	client := startServer(t, &stubHealthServer{}, WithAuth(v), WithOptionalAuthMethods(checkMethod))
	resp, err := client.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("optional method should allow anonymous: %v", err)
	}
	if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status = %v, want NOT_SERVING (anonymous => no principal)", resp.GetStatus())
	}
}

func TestNewServer_WithAuth_ValidTokenInjectsPrincipal(t *testing.T) {
	v, key := testVerifier(t)
	client := startServer(t, &stubHealthServer{}, WithAuth(v))
	raw := signToken(t, key, "user-1")

	resp, err := client.Check(withBearer(t.Context(), raw), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check with a valid token: %v", err)
	}
	if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING (principal should be on the handler's context)", resp.GetStatus())
	}
}

func TestNewServer_WithAuth_OptionalMethodStillVerifiesAPresentToken(t *testing.T) {
	// "optional" means anonymous is *allowed*, not that a token present is
	// never checked — an invalid token on an optional method must still fail.
	v, _ := testVerifier(t)
	client := startServer(t, &stubHealthServer{}, WithAuth(v), WithOptionalAuthMethods(checkMethod))
	_, err := client.Check(withBearer(t.Context(), "not-a-jwt"), &grpc_health_v1.HealthCheckRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("err = %v, want Unauthenticated for a garbage token even on an optional method", err)
	}
}

func TestNewServer_WithAuth_InvalidTokenRejected(t *testing.T) {
	v, _ := testVerifier(t)
	client := startServer(t, &stubHealthServer{}, WithAuth(v))
	_, err := client.Check(withBearer(t.Context(), "garbage"), &grpc_health_v1.HealthCheckRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("err = %v, want Unauthenticated", err)
	}
}

func TestNewServer_PanicInHandlerRecoveredAsInternal(t *testing.T) {
	client := startServer(t, &stubHealthServer{panicOnCheck: true})
	_, err := client.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("err = %v, want Internal (recovered panic)", err)
	}
}

func TestNewServer_WithUnaryInterceptor_RunsAfterTheStandardStack(t *testing.T) {
	var sawContext bool
	extra := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		sawContext = true
		return handler(ctx, req)
	}
	client := startServer(t, &stubHealthServer{}, WithUnaryInterceptor(extra))
	if _, err := client.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !sawContext {
		t.Fatal("the extra interceptor from WithUnaryInterceptor never ran")
	}
}

func TestForwardAuth_PropagatesTheCallersTokenDownstream(t *testing.T) {
	// End-to-end: a "downstream" server behind an auth-required method, dialed
	// with ForwardAuth() from a context that already carries an incoming
	// bearer — the same shape services/order uses to call inventory/payment.
	v, key := testVerifier(t)
	lis := bufconn.Listen(1024 * 1024)
	srv := NewServer(WithAuth(v))
	grpc_health_v1.RegisterHealthServer(srv, &stubHealthServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := Dial("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		ForwardAuth(),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := grpc_health_v1.NewHealthClient(conn)

	raw := signToken(t, key, "user-1")
	// Simulate being inside a handler that received this bearer as incoming
	// metadata (what a real service's own authUnary would have set up).
	inbound := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+raw))

	resp, err := client.Check(inbound, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check via a ForwardAuth-dialed connection: %v", err)
	}
	if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatal("the downstream server never saw a principal — ForwardAuth did not forward the token")
	}
}

// fakeServerStream implements just enough of grpc.ServerStream for
// recoveryStream, which only ever calls Context() on it.
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f fakeServerStream) Context() context.Context { return f.ctx }

func TestRecoveryStream_ConvertsPanicToInternalError(t *testing.T) {
	interceptor := recoveryStream()
	err := interceptor(nil, fakeServerStream{ctx: t.Context()}, &grpc.StreamServerInfo{FullMethod: "/svc/Stream"},
		func(srv any, ss grpc.ServerStream) error { panic("boom") })
	if status.Code(err) != codes.Internal {
		t.Fatalf("err = %v, want Internal", err)
	}
}

func TestRecoveryStream_PassesThroughSuccess(t *testing.T) {
	interceptor := recoveryStream()
	err := interceptor(nil, fakeServerStream{ctx: t.Context()}, &grpc.StreamServerInfo{FullMethod: "/svc/Stream"},
		func(srv any, ss grpc.ServerStream) error { return nil })
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestServe_FailsFastOnAnUnusableAddress(t *testing.T) {
	srv := NewServer()
	err := Serve(t.Context(), srv, "this-is-not-a-valid-address:::")
	if err == nil {
		t.Fatal("expected net.Listen to fail on an unparseable address")
	}
}

func TestServe_StopsOnContextCancellation(t *testing.T) {
	srv := NewServer()
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv, "127.0.0.1:0") }()

	// Give Serve a moment to reach the blocking Accept loop, then cancel —
	// this exercises the goroutine that calls GracefulStop on ctx.Done().
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its context was cancelled")
	}
}

func TestLoggingUnary_MapsDomainErrorAndPassesThroughSuccess(t *testing.T) {
	ok := loggingUnary()
	resp, err := ok(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/M"},
		func(ctx context.Context, req any) (any, error) { return "fine", nil })
	if err != nil || resp != "fine" {
		t.Fatalf("success path: resp=%v err=%v", resp, err)
	}

	failing := loggingUnary()
	_, err = failing(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/M"},
		func(ctx context.Context, req any) (any, error) {
			return nil, errs.New(errs.KindNotFound, "THING_NOT_FOUND", "internal detail nobody outside should see")
		})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("err code = %v, want NotFound", status.Code(err))
	}
}
