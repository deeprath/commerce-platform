package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The BFF only ever serves JSON that is never framed and never cached; these
// headers are asserted by the ZAP authenticated scan and easy to regress.
func TestSecureHeaders_OnEveryResponse(t *testing.T) {
	s := &Server{}
	for _, path := range []string{"/healthz", "/api/v1/orders" /* 401, still passes through the mw */} {
		rec := httptest.NewRecorder()
		s.Router([]string{"*"}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		for h, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "no-referrer",
			"Cache-Control":          "no-store",
		} {
			if got := rec.Header().Get(h); got != want {
				t.Errorf("%s: header %s = %q, want %q", path, h, got, want)
			}
		}
	}
}
