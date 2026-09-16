package api

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/deeprath/commerce-platform/services/bff/internal/auth"
)

// bffWithIdP wires a Server to a stub Keycloak and serves both over real
// listeners, so the assertions below run against actual HTTP rather than a
// recorder.
func bffWithIdP(t *testing.T, idpStatus int, idpBody string) (*httptest.Server, *http.Client) {
	t.Helper()

	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(idpStatus)
		_, _ = w.Write([]byte(idpBody))
	}))
	t.Cleanup(idp.Close)

	s := &Server{broker: auth.NewBroker(idp.URL, "storefront", "s3cret", false)}
	bff := httptest.NewServer(s.Router([]string{"*"}))
	t.Cleanup(bff.Close)

	// A real cookie jar, so cookies are stored and expired by RFC 6265's rules —
	// including the path matching that a ResponseRecorder can't show us.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return bff, &http.Client{Jar: jar}
}

// post returns the status code; no test here cares about a response body, and
// closing it inside keeps every caller from having to.
func post(t *testing.T, c *http.Client, url, body string) int {
	t.Helper()
	resp, err := c.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// cookiesAt reports what the browser would send to a given path.
func cookiesAt(t *testing.T, c *http.Client, base, path string) map[string]string {
	t.Helper()
	u, err := url.Parse(base + path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := map[string]string{}
	for _, ck := range c.Jar.Cookies(u) {
		out[ck.Name] = ck.Value
	}
	return out
}

const idpToken = `{"access_token":"at-1","refresh_token":"rt-1","expires_in":300}`

// Logging out must leave the browser holding neither token. The refresh cookie
// is set at a narrower path than the access cookie, and a cookie is identified
// by name *and* path — so clearing both at "/" used to leave the refresh token
// alive and still attached to every request to the auth routes.
func TestLogout_LeavesNoTokenBehind(t *testing.T) {
	bff, client := bffWithIdP(t, 200, idpToken)

	if got := post(t, client, bff.URL+"/api/v1/auth/login", `{"username":"ada","password":"hunter2"}`); got != 200 {
		t.Fatalf("login status = %d", got)
	}
	// Precondition: the login actually put both tokens in the jar, so a clean
	// jar after logout means they were removed rather than never stored.
	if got := cookiesAt(t, client, bff.URL, "/api/v1/orders"); got["access_token"] != "at-1" {
		t.Fatalf("access_token not held after login: %v", got)
	}
	if got := cookiesAt(t, client, bff.URL, "/api/v1/auth/logout"); got["refresh_token"] != "rt-1" {
		t.Fatalf("refresh_token not held after login: %v", got)
	}

	if got := post(t, client, bff.URL+"/api/v1/auth/logout", ""); got != 204 {
		t.Fatalf("logout status = %d", got)
	}

	// Check the auth path specifically: it is the only place the refresh cookie
	// was ever sent, so it is the only place its survival would show.
	for _, path := range []string{"/", "/api/v1/orders", "/api/v1/auth", "/api/v1/auth/refresh"} {
		if got := cookiesAt(t, client, bff.URL, path); len(got) != 0 {
			t.Errorf("after logout the browser still sends %v to %s", got, path)
		}
	}
}

func TestLogin_RequiresBothFields(t *testing.T) {
	bff, client := bffWithIdP(t, 200, idpToken)

	for _, body := range []string{`{}`, `{"username":"ada"}`, `{"password":"hunter2"}`, `not json`} {
		if got := post(t, client, bff.URL+"/api/v1/auth/login", body); got != 400 {
			t.Errorf("body %q: status = %d, want 400", body, got)
		}
		if got := cookiesAt(t, client, bff.URL, "/"); len(got) != 0 {
			t.Errorf("body %q: a rejected login set cookies: %v", body, got)
		}
	}
}

// Keycloak rejecting the password has to reach the browser as 401, not as the
// 503 that a provider outage produces — the storefront shows a different
// message for each.
func TestLogin_BadCredentialsAre401(t *testing.T) {
	bff, client := bffWithIdP(t, http.StatusUnauthorized, `{"error":"invalid_grant"}`)

	if got := post(t, client, bff.URL+"/api/v1/auth/login", `{"username":"ada","password":"wrong"}`); got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got)
	}
	if got := cookiesAt(t, client, bff.URL, "/"); len(got) != 0 {
		t.Fatalf("a failed login set cookies: %v", got)
	}
}

func TestLogin_ProviderOutageIsNot401(t *testing.T) {
	bff, client := bffWithIdP(t, http.StatusInternalServerError, `{}`)

	got := post(t, client, bff.URL+"/api/v1/auth/login", `{"username":"ada","password":"hunter2"}`)
	if got == http.StatusUnauthorized {
		t.Fatal("an identity-provider outage was reported as bad credentials")
	}
	if got != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", got)
	}
}

// Logout is what a user reaches for when they think something is wrong, so it
// must work even for a browser holding nothing.
func TestLogout_WithoutASessionStillSucceeds(t *testing.T) {
	bff, client := bffWithIdP(t, 200, idpToken)

	if got := post(t, client, bff.URL+"/api/v1/auth/logout", ""); got != 204 {
		t.Fatalf("status = %d, want 204", got)
	}
}
