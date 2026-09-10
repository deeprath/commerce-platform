package api

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"google.golang.org/protobuf/proto"

	clickstreamv1 "github.com/deeprath/commerce-platform/gen/go/commerce/clickstream/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
)

const (
	anonCookieName   = "cid"
	anonCookieMaxAge = 180 * 24 * 60 * 60 // seconds (~6 months)
	maxBeaconEvents  = 20
	maxFieldBytes    = 512
	maxUABytes       = 256
)

// clientEvent is the loose wire shape the storefront posts. Unknown types and
// oversized fields are dropped, never 400'd, so a stale SPA build can't poison
// the whole beacon.
type clientEvent struct {
	Type       string `json:"type"`
	SessionID  string `json:"session_id"`
	Path       string `json:"path"`
	Referrer   string `json:"referrer"`
	ProductID  string `json:"product_id"`
	Query      string `json:"query"`
	ValueMinor int64  `json:"value_minor"`
	Currency   string `json:"currency_code"`
	ClientTime string `json:"client_time"`
}

type beaconBody struct {
	Events []clientEvent `json:"events"`
}

var clientEventTypes = map[string]clickstreamv1.EventType{
	"page_view":      clickstreamv1.EventType_EVENT_TYPE_PAGE_VIEW,
	"product_view":   clickstreamv1.EventType_EVENT_TYPE_PRODUCT_VIEW,
	"search":         clickstreamv1.EventType_EVENT_TYPE_SEARCH,
	"add_to_cart":    clickstreamv1.EventType_EVENT_TYPE_ADD_TO_CART,
	"begin_checkout": clickstreamv1.EventType_EVENT_TYPE_BEGIN_CHECKOUT,
	"purchase":       clickstreamv1.EventType_EVENT_TYPE_PURCHASE,
}

// ingestEvents accepts a batch of browser events (navigator.sendBeacon), stamps
// the fields only the server can be trusted for, and forwards them to Kafka.
// Answers 202 for anything with a parseable body — the client ignores the
// response — and 503 when no publisher is wired.
func (s *Server) ingestEvents(c echo.Context) error {
	if s.pub == nil {
		return c.NoContent(http.StatusServiceUnavailable)
	}
	body, err := readBody(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errs.HTTPError{Status: 400, Code: "INVALID_ARGUMENT", Reason: "BODY_READ"})
	}
	var in beaconBody
	if err := json.Unmarshal(body, &in); err != nil {
		return c.JSON(http.StatusBadRequest, errs.HTTPError{Status: 400, Code: "INVALID_ARGUMENT", Reason: "BAD_JSON"})
	}
	if len(in.Events) > maxBeaconEvents {
		in.Events = in.Events[:maxBeaconEvents]
	}

	anon := s.anonID(c)
	owner := subFromJWT(bearer(c))
	ua := truncateBytes(c.Request().UserAgent(), maxUABytes)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ctx := c.Request().Context()

	accepted := 0
	for _, e := range in.Events {
		kind, ok := clientEventTypes[e.Type]
		if !ok {
			continue
		}
		msg := &clickstreamv1.ClientEvent{
			Type:         kind,
			SessionId:    truncateBytes(e.SessionID, 64),
			Path:         truncateBytes(pathOnly(e.Path), maxFieldBytes),
			Referrer:     truncateBytes(hostOnly(e.Referrer), maxFieldBytes),
			ProductId:    truncateBytes(e.ProductID, 64),
			Query:        truncateBytes(e.Query, maxFieldBytes),
			AnonymousId:  anon,
			OwnerId:      owner,
			UserAgent:    ua,
			ValueMinor:   e.ValueMinor,
			CurrencyCode: truncateBytes(e.Currency, 3),
			ClientTime:   truncateBytes(e.ClientTime, 40),
			ReceivedAt:   now,
		}
		v, err := proto.Marshal(msg)
		if err != nil {
			continue
		}
		if err := s.pub.Publish(ctx, anon, v); err != nil {
			slog.ErrorContext(ctx, "clickstream publish failed", slog.Any("err", err))
			break // best-effort; report how many made it
		}
		accepted++
	}
	return c.JSON(http.StatusAccepted, map[string]int{"accepted": accepted})
}

// anonID reads the first-party visitor cookie, minting and setting one when it
// is missing or malformed. The value is an opaque random UUID, never linked to
// PII — it only powers unique-visitor counts.
func (s *Server) anonID(c echo.Context) string {
	if ck, err := c.Cookie(anonCookieName); err == nil {
		if _, err := uuid.Parse(ck.Value); err == nil {
			return ck.Value
		}
	}
	id := uuid.NewString()
	c.SetCookie(&http.Cookie{
		Name:     anonCookieName,
		Value:    id,
		Path:     "/",
		MaxAge:   anonCookieMaxAge,
		HttpOnly: true,
		Secure:   c.Scheme() == "https",
		SameSite: http.SameSiteLaxMode,
	})
	return id
}

// subFromJWT pulls the "sub" claim out of a bearer token WITHOUT verifying the
// signature. This is deliberate: the value is used only to attribute analytics
// rows, never for authorization, and a forged sub only pollutes the forger's
// own funnel. Returns "" for anything that doesn't cleanly decode.
func subFromJWT(tok string) string {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return ""
	}
	return claims.Sub
}

// pathOnly keeps the path component of a client-supplied URL/path, dropping any
// query string or fragment. A value that isn't an absolute path is discarded.
func pathOnly(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if !strings.HasPrefix(s, "/") {
		return ""
	}
	return s
}

// hostOnly reduces a referrer to its host, so we never store where on another
// site the visitor came from.
func hostOnly(s string) string {
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Host
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
