// Package auth brokers the custom login: the BFF exchanges username/password
// with Keycloak (confidential client, Resource Owner grant) and hands the
// browser an httpOnly cookie — the token never reaches JavaScript.
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

type Broker struct {
	tokenURL     string
	clientID     string
	clientSecret string
	hc           *http.Client
	cookieSecure bool
}

func NewBroker(tokenURL, clientID, clientSecret string, cookieSecure bool) *Broker {
	return &Broker{
		tokenURL: tokenURL, clientID: clientID, clientSecret: clientSecret,
		hc:           &http.Client{Timeout: 8 * time.Second},
		cookieSecure: cookieSecure,
	}
}

// CookieSecure reports the Secure flag every cookie the BFF sets should carry
// (true in every deployment except local http dev). Other cookie-setting code
// in the BFF (e.g. the cart id cookie) shares this instead of hardcoding its
// own. Safe on a nil receiver (tests construct a Server without a Broker).
func (b *Broker) CookieSecure() bool { return b != nil && b.cookieSecure }

type TokenResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

// Login exchanges credentials for tokens via Keycloak's token endpoint.
func (b *Broker) Login(ctx context.Context, username, password string) (*TokenResult, error) {
	form := url.Values{
		"grant_type":    {"password"},
		"client_id":     {b.clientID},
		"client_secret": {b.clientSecret},
		"username":      {username},
		"password":      {password},
		"scope":         {"openid"},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, b.tokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := b.hc.Do(req)
	if err != nil {
		return nil, errs.Wrap(err, errs.KindUnavailable, "IDP_UNREACHABLE", "cannot reach the identity provider")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest {
		return nil, errs.New(errs.KindUnauthenticated, "BAD_CREDENTIALS", "invalid username or password")
	}
	if resp.StatusCode >= 300 {
		return nil, errs.New(errs.KindUnavailable, "IDP_ERROR", "identity provider returned an error")
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, errs.Wrap(err, errs.KindInternal, "IDP_DECODE", "cannot decode token response")
	}
	return &TokenResult{AccessToken: body.AccessToken, RefreshToken: body.RefreshToken, ExpiresIn: body.ExpiresIn}, nil
}

// SetCookies writes the access (and refresh) tokens as httpOnly cookies.
func (b *Broker) SetCookies(w http.ResponseWriter, t *TokenResult) {
	ttl := t.ExpiresIn
	if ttl <= 0 {
		ttl = 300
	}
	http.SetCookie(w, &http.Cookie{
		Name: "access_token", Value: t.AccessToken, Path: "/",
		HttpOnly: true, Secure: b.cookieSecure, SameSite: http.SameSiteLaxMode,
		MaxAge: ttl,
	})
	if t.RefreshToken != "" {
		http.SetCookie(w, &http.Cookie{
			Name: "refresh_token", Value: t.RefreshToken, Path: "/api/v1/auth",
			HttpOnly: true, Secure: b.cookieSecure, SameSite: http.SameSiteLaxMode,
			MaxAge: 1800,
		})
	}
}

// ClearCookies expires the auth cookies. Secure must match what SetCookies used
// — some browsers won't let a non-Secure Set-Cookie overwrite/delete a cookie
// that was set Secure.
func (b *Broker) ClearCookies(w http.ResponseWriter) {
	for _, n := range []string{"access_token", "refresh_token"} {
		http.SetCookie(w, &http.Cookie{Name: n, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: b.cookieSecure})
	}
}
