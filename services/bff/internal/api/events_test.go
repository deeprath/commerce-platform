package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	clickstreamv1 "github.com/deeprath/commerce-platform/gen/go/commerce/clickstream/v1"
)

type fakePublisher struct {
	got  [][]byte
	keys []string
	err  error
}

func (f *fakePublisher) Publish(_ context.Context, key string, value []byte) error {
	if f.err != nil {
		return f.err
	}
	f.keys = append(f.keys, key)
	f.got = append(f.got, value)
	return nil
}

func postEvents(t *testing.T, s *Server, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Router([]string{"*"}).ServeHTTP(rec, req)
	return rec
}

func decodeEvents(t *testing.T, f *fakePublisher) []*clickstreamv1.ClientEvent {
	t.Helper()
	out := make([]*clickstreamv1.ClientEvent, 0, len(f.got))
	for _, b := range f.got {
		var e clickstreamv1.ClientEvent
		if err := proto.Unmarshal(b, &e); err != nil {
			t.Fatalf("unmarshal published event: %v", err)
		}
		out = append(out, &e)
	}
	return out
}

func TestIngestEvents_NoPublisher503(t *testing.T) {
	rec := postEvents(t, &Server{}, `{"events":[{"type":"page_view"}]}`, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestIngestEvents_BadJSON400(t *testing.T) {
	rec := postEvents(t, &Server{pub: &fakePublisher{}}, `{not json`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestIngestEvents_EnrichesAndForwardsKnownTypes(t *testing.T) {
	f := &fakePublisher{}
	body := `{"events":[
		{"type":"page_view","path":"/p/acme?ref=x#frag","referrer":"https://google.com/search?q=z","client_time":"2026-09-10T00:00:00Z"},
		{"type":"product_view","product_id":"prod-1"},
		{"type":"nonsense"},
		{"type":"search","query":"lamp"}
	]}`
	rec := postEvents(t, &Server{pub: f}, body, map[string]string{"User-Agent": "curl/8"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	var resp struct {
		Accepted int `json:"accepted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Accepted != 3 {
		t.Fatalf("accepted = %d, want 3 (the unknown type is dropped)", resp.Accepted)
	}
	evs := decodeEvents(t, f)
	if len(evs) != 3 {
		t.Fatalf("published %d events, want 3", len(evs))
	}
	if evs[0].GetType() != clickstreamv1.EventType_EVENT_TYPE_PAGE_VIEW {
		t.Errorf("event 0 type = %v", evs[0].GetType())
	}
	if evs[0].GetPath() != "/p/acme" {
		t.Errorf("path not stripped of query/fragment: %q", evs[0].GetPath())
	}
	if evs[0].GetReferrer() != "google.com" {
		t.Errorf("referrer not reduced to host: %q", evs[0].GetReferrer())
	}
	if evs[0].GetUserAgent() != "curl/8" {
		t.Errorf("user agent = %q", evs[0].GetUserAgent())
	}
	if evs[0].GetReceivedAt() == "" {
		t.Error("received_at not stamped")
	}
	if evs[1].GetProductId() != "prod-1" {
		t.Errorf("product_id = %q", evs[1].GetProductId())
	}
	// Every event in a beacon shares one anonymous id (and it's the publish key).
	if a, b := evs[0].GetAnonymousId(), evs[2].GetAnonymousId(); a == "" || a != b {
		t.Errorf("anonymous ids not shared: %q vs %q", a, b)
	}
	for _, k := range f.keys {
		if k != evs[0].GetAnonymousId() {
			t.Errorf("publish key %q != anonymous id %q", k, evs[0].GetAnonymousId())
		}
	}
}

func TestIngestEvents_MintsAndReusesCookie(t *testing.T) {
	f := &fakePublisher{}
	rec := postEvents(t, &Server{pub: f}, `{"events":[{"type":"page_view"}]}`, nil)
	var setCookie string
	for _, c := range rec.Result().Cookies() {
		if c.Name == anonCookieName {
			setCookie = c.Value
			if !c.HttpOnly {
				t.Error("cid cookie must be HttpOnly")
			}
		}
	}
	if setCookie == "" {
		t.Fatal("no cid cookie minted on first request")
	}
	// Second request carrying the cookie must not mint a new one and must reuse the id.
	f2 := &fakePublisher{}
	rec2 := postEvents(t, &Server{pub: f2}, `{"events":[{"type":"page_view"}]}`,
		map[string]string{"Cookie": anonCookieName + "=" + setCookie})
	for _, c := range rec2.Result().Cookies() {
		if c.Name == anonCookieName {
			t.Error("cid cookie re-minted when a valid one was presented")
		}
	}
	if got := decodeEvents(t, f2)[0].GetAnonymousId(); got != setCookie {
		t.Fatalf("anonymous id = %q, want the presented cookie %q", got, setCookie)
	}
}

func TestIngestEvents_CapsBatchAndStopsOnPublishError(t *testing.T) {
	// Cap: 25 in, only maxBeaconEvents forwarded.
	var sb strings.Builder
	sb.WriteString(`{"events":[`)
	for i := 0; i < 25; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"type":"page_view"}`)
	}
	sb.WriteString(`]}`)
	f := &fakePublisher{}
	postEvents(t, &Server{pub: f}, sb.String(), nil)
	if len(f.got) != maxBeaconEvents {
		t.Fatalf("forwarded %d, want the cap %d", len(f.got), maxBeaconEvents)
	}

	// Publish error: 202 with accepted:0, loop broken.
	fe := &fakePublisher{err: errors.New("kafka down")}
	rec := postEvents(t, &Server{pub: fe}, `{"events":[{"type":"page_view"},{"type":"search"}]}`, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 even on publish failure", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"accepted":0`) {
		t.Fatalf("body = %s, want accepted:0", rec.Body.String())
	}
}

func TestSubFromJWT(t *testing.T) {
	mk := func(payload string) string {
		return "h." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
	}
	cases := map[string]string{
		mk(`{"sub":"user-42","iss":"kc"}`): "user-42",
		mk(`{"iss":"kc"}`):                 "",
		"not.a.jwt":                        "",
		"only-one-segment":                 "",
		"h.%%%.s":                          "",
	}
	for tok, want := range cases {
		if got := subFromJWT(tok); got != want {
			t.Errorf("subFromJWT(%q) = %q, want %q", tok, got, want)
		}
	}
}

func TestPathAndHostHelpers(t *testing.T) {
	if got := pathOnly("/a/b?x=1#y"); got != "/a/b" {
		t.Errorf("pathOnly = %q", got)
	}
	if got := pathOnly("https://evil.com/x"); got != "" {
		t.Errorf("pathOnly kept a non-absolute path: %q", got)
	}
	if got := hostOnly("https://ref.example.com/deep/link?q=1"); got != "ref.example.com" {
		t.Errorf("hostOnly = %q", got)
	}
	if got := hostOnly(""); got != "" {
		t.Errorf("hostOnly(empty) = %q", got)
	}
	if got := truncateBytes("abcdef", 3); got != "abc" {
		t.Errorf("truncateBytes = %q", got)
	}
	if got := truncateBytes("abc", 10); got != "abc" {
		t.Errorf("truncateBytes short-circuit = %q", got)
	}
	// A multi-byte rune straddling the cut is trimmed rather than left invalid.
	if got := truncateBytes("aé", 2); got != "a" {
		t.Errorf("truncateBytes split a rune: %q", got)
	}
}
