package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/bff/internal/auth"
)

// idp stands in for Keycloak's token endpoint.
func idp(t *testing.T, status int, body string, capture *url.Values) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			*capture = r.PostForm
			capture.Set("__content_type", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const tokenJSON = `{"access_token":"at-1","refresh_token":"rt-1","expires_in":300}`

func TestLogin_SendsTheResourceOwnerGrant(t *testing.T) {
	var form url.Values
	srv := idp(t, 200, tokenJSON, &form)
	b := auth.NewBroker(srv.URL, "storefront", "s3cret", false)

	tok, err := b.Login(context.Background(), "ada", "hunter2")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if tok.AccessToken != "at-1" || tok.RefreshToken != "rt-1" || tok.ExpiresIn != 300 {
		t.Fatalf("token = %+v", tok)
	}
	for k, want := range map[string]string{
		"grant_type":     "password",
		"client_id":      "storefront",
		"client_secret":  "s3cret",
		"username":       "ada",
		"password":       "hunter2",
		"scope":          "openid",
		"__content_type": "application/x-www-form-urlencoded",
	} {
		if got := form.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// Both of Keycloak's rejection codes must read as bad credentials, and neither
// may leak whether the account exists.
func TestLogin_RejectionsAreIndistinguishable(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusBadRequest} {
		srv := idp(t, status, `{"error":"invalid_grant","error_description":"Account disabled"}`, nil)
		b := auth.NewBroker(srv.URL, "storefront", "s3cret", false)

		_, err := b.Login(context.Background(), "ada", "wrong")
		if !errs.Is(err, errs.KindUnauthenticated) {
			t.Fatalf("status %d: err = %v, want Unauthenticated", status, err)
		}
		if strings.Contains(err.Error(), "disabled") {
			t.Fatalf("status %d: the provider's reason leaked to the caller: %v", status, err)
		}
	}
}

// A broken identity provider is not a failed login: answering Unauthenticated
// would tell the shopper their password is wrong when it isn't.
func TestLogin_ProviderFailureIsNotACredentialFailure(t *testing.T) {
	srv := idp(t, http.StatusInternalServerError, `{}`, nil)
	b := auth.NewBroker(srv.URL, "storefront", "s3cret", false)

	_, err := b.Login(context.Background(), "ada", "hunter2")
	if !errs.Is(err, errs.KindUnavailable) {
		t.Fatalf("err = %v, want Unavailable", err)
	}
}

func TestLogin_UnreachableProvider(t *testing.T) {
	// A port nothing is listening on.
	b := auth.NewBroker("http://127.0.0.1:1/token", "storefront", "s3cret", false)
	_, err := b.Login(context.Background(), "ada", "hunter2")
	if !errs.Is(err, errs.KindUnavailable) {
		t.Fatalf("err = %v, want Unavailable", err)
	}
}

func TestLogin_UndecodableResponse(t *testing.T) {
	srv := idp(t, 200, `not json`, nil)
	b := auth.NewBroker(srv.URL, "storefront", "s3cret", false)

	if _, err := b.Login(context.Background(), "ada", "hunter2"); err == nil {
		t.Fatal("a non-JSON token response was accepted")
	}
}

func cookiesFrom(rec *httptest.ResponseRecorder) map[string]*http.Cookie {
	out := map[string]*http.Cookie{}
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		out[c.Name] = c
	}
	return out
}

// The point of brokering the login server-side: the token must never be
// reachable from JavaScript.
func TestSetCookies_TokensAreHttpOnly(t *testing.T) {
	b := auth.NewBroker("http://idp", "storefront", "s3cret", true)
	rec := httptest.NewRecorder()
	b.SetCookies(rec, &auth.TokenResult{AccessToken: "at-1", RefreshToken: "rt-1", ExpiresIn: 300})

	got := cookiesFrom(rec)
	for _, name := range []string{"access_token", "refresh_token"} {
		c, ok := got[name]
		if !ok {
			t.Fatalf("%s cookie not set", name)
		}
		if !c.HttpOnly {
			t.Errorf("%s is readable from JavaScript", name)
		}
		if !c.Secure {
			t.Errorf("%s was not marked Secure despite cookieSecure", name)
		}
		if c.SameSite != http.SameSiteLaxMode {
			t.Errorf("%s SameSite = %v", name, c.SameSite)
		}
	}
}

// The refresh token is scoped more narrowly than the access token, so it is not
// attached to every request the browser makes.
func TestSetCookies_RefreshTokenIsScopedToTheAuthPath(t *testing.T) {
	b := auth.NewBroker("http://idp", "storefront", "s3cret", false)
	rec := httptest.NewRecorder()
	b.SetCookies(rec, &auth.TokenResult{AccessToken: "at-1", RefreshToken: "rt-1", ExpiresIn: 300})

	got := cookiesFrom(rec)
	if got["access_token"].Path != "/" {
		t.Errorf("access_token path = %q, want /", got["access_token"].Path)
	}
	if got["refresh_token"].Path != "/api/v1/auth" {
		t.Errorf("refresh_token path = %q, want it scoped to the auth routes", got["refresh_token"].Path)
	}
}

func TestSetCookies_NoRefreshCookieWithoutARefreshToken(t *testing.T) {
	b := auth.NewBroker("http://idp", "storefront", "s3cret", false)
	rec := httptest.NewRecorder()
	b.SetCookies(rec, &auth.TokenResult{AccessToken: "at-1", ExpiresIn: 300})

	if _, ok := cookiesFrom(rec)["refresh_token"]; ok {
		t.Fatal("an empty refresh token was written as a cookie")
	}
}

// A provider that omits expires_in must not produce a session cookie that never
// expires.
func TestSetCookies_MissingExpiryFallsBackToAShortTTL(t *testing.T) {
	b := auth.NewBroker("http://idp", "storefront", "s3cret", false)
	rec := httptest.NewRecorder()
	b.SetCookies(rec, &auth.TokenResult{AccessToken: "at-1"})

	if age := cookiesFrom(rec)["access_token"].MaxAge; age != 300 {
		t.Fatalf("MaxAge = %d, want the 300s fallback", age)
	}
}

// Logging out has to actually remove both tokens. A cookie is identified by its
// name *and* path, so clearing one at a path it was never set on leaves the real
// cookie in the browser — still sent on every request to the auth routes.
func TestClearCookies_ExpiresBothTokensAtThePathsTheyWereSetOn(t *testing.T) {
	b := auth.NewBroker("http://idp", "storefront", "s3cret", true)

	set := httptest.NewRecorder()
	b.SetCookies(set, &auth.TokenResult{AccessToken: "at-1", RefreshToken: "rt-1", ExpiresIn: 300})
	setPaths := map[string]string{}
	for name, c := range cookiesFrom(set) {
		setPaths[name] = c.Path
	}

	clear := httptest.NewRecorder()
	b.ClearCookies(clear)
	cleared := cookiesFrom(clear)

	for name, path := range setPaths {
		c, ok := cleared[name]
		if !ok {
			t.Fatalf("%s was never cleared", name)
		}
		if c.Path != path {
			t.Errorf("%s cleared at path %q but set at %q — the real cookie survives logout", name, c.Path, path)
		}
		if c.MaxAge != -1 {
			t.Errorf("%s MaxAge = %d, want -1 to expire it", name, c.MaxAge)
		}
		// Some browsers refuse a non-Secure Set-Cookie that overwrites a Secure
		// one, so the flag has to match what set it.
		if !c.Secure {
			t.Errorf("%s cleared without Secure, which set it", name)
		}
	}
}

// Server is constructed without a Broker in tests, so this must not panic.
func TestCookieSecure_IsNilSafe(t *testing.T) {
	var b *auth.Broker
	if b.CookieSecure() {
		t.Fatal("a nil broker claimed Secure cookies")
	}
	if !auth.NewBroker("", "", "", true).CookieSecure() {
		t.Fatal("cookieSecure not reported")
	}
}
