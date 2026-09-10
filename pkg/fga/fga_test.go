package fga_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/deeprath/commerce-platform/pkg/fga"
)

const testModel = `{"schema_version":"1.1","type_definitions":[{"type":"user"},{"type":"order","relations":{"viewer":{"this":{}}},"metadata":{"relations":{"viewer":{"directly_related_user_types":[{"type":"user"}]}}}}]}`

// fakeFGA is a minimal in-memory stand-in for the OpenFGA HTTP API: enough of
// stores / authorization-models / write / read / check / list-objects to drive
// the client.
type fakeFGA struct {
	mu     sync.Mutex
	stores map[string]string // id -> name
	models map[string][]string
	tuples map[string]map[string]bool // storeID -> "user|relation|object" -> true
	nextID int
}

func newFakeFGA() *fakeFGA {
	return &fakeFGA{stores: map[string]string{}, models: map[string][]string{}, tuples: map[string]map[string]bool{}}
}

func (f *fakeFGA) id(prefix string) string {
	f.nextID++
	return prefix + "-" + string(rune('a'+f.nextID))
}

func writeJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

func badTuple(w http.ResponseWriter, msg string) {
	w.WriteHeader(http.StatusBadRequest)
	writeJSON(w, map[string]string{"code": "write_failed_due_to_invalid_input", "message": msg})
}

func (f *fakeFGA) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/stores", f.locked(f.handleStores))
	mux.HandleFunc("/stores/", f.locked(f.handleStoreScoped))
	return mux
}

// locked wraps a handler in the fake's mutex.
func (f *fakeFGA) locked(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		h(w, r)
	}
}

func (f *fakeFGA) handleStores(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		var out struct {
			Stores []map[string]string `json:"stores"`
		}
		for id, name := range f.stores {
			out.Stores = append(out.Stores, map[string]string{"id": id, "name": name})
		}
		writeJSON(w, out)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := f.id("store")
	f.stores[id] = body["name"].(string)
	writeJSON(w, map[string]string{"id": id, "name": f.stores[id]})
}

func (f *fakeFGA) handleStoreScoped(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/stores/"), "/")
	storeID, action := parts[0], ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch action {
	case "authorization-models":
		f.handleModels(w, r, storeID)
	case "write":
		f.handleWrite(w, r, storeID)
	case "check":
		f.handleCheck(w, r, storeID)
	case "list-objects":
		f.handleListObjects(w, r, storeID)
	case "read":
		f.handleRead(w, r, storeID)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeFGA) handleModels(w http.ResponseWriter, r *http.Request, storeID string) {
	if r.Method == http.MethodPost {
		mid := f.id("model")
		f.models[storeID] = append([]string{mid}, f.models[storeID]...)
		writeJSON(w, map[string]string{"authorization_model_id": mid})
		return
	}
	var out struct {
		AuthorizationModels []map[string]string `json:"authorization_models"`
	}
	for _, m := range f.models[storeID] {
		out.AuthorizationModels = append(out.AuthorizationModels, map[string]string{"id": m})
	}
	writeJSON(w, out)
}

func (f *fakeFGA) handleWrite(w http.ResponseWriter, r *http.Request, storeID string) {
	var body struct {
		Writes, Deletes struct {
			TupleKeys []struct{ User, Relation, Object string } `json:"tuple_keys"`
		}
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if f.tuples[storeID] == nil {
		f.tuples[storeID] = map[string]bool{}
	}
	for _, tk := range body.Writes.TupleKeys {
		k := tk.User + "|" + tk.Relation + "|" + tk.Object
		if f.tuples[storeID][k] {
			badTuple(w, "tuple already exists")
			return
		}
		f.tuples[storeID][k] = true
	}
	for _, tk := range body.Deletes.TupleKeys {
		k := tk.User + "|" + tk.Relation + "|" + tk.Object
		if !f.tuples[storeID][k] {
			badTuple(w, "cannot delete a tuple which does not exist: not found")
			return
		}
		delete(f.tuples[storeID], k)
	}
	_, _ = w.Write([]byte(`{}`))
}

func (f *fakeFGA) handleCheck(w http.ResponseWriter, r *http.Request, storeID string) {
	var body struct {
		TupleKey struct{ User, Relation, Object string } `json:"tuple_key"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	k := body.TupleKey.User + "|" + body.TupleKey.Relation + "|" + body.TupleKey.Object
	writeJSON(w, map[string]bool{"allowed": f.tuples[storeID][k]})
}

func (f *fakeFGA) handleListObjects(w http.ResponseWriter, r *http.Request, storeID string) {
	var body struct{ Type, Relation, User string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	var out struct {
		Objects []string `json:"objects"`
	}
	for k := range f.tuples[storeID] {
		p := strings.Split(k, "|")
		if p[0] == body.User && p[1] == body.Relation && strings.HasPrefix(p[2], body.Type+":") {
			out.Objects = append(out.Objects, p[2])
		}
	}
	writeJSON(w, out)
}

func (f *fakeFGA) handleRead(w http.ResponseWriter, r *http.Request, storeID string) {
	var body struct {
		TupleKey struct{ Object string } `json:"tuple_key"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var out struct {
		Tuples []map[string]any `json:"tuples"`
	}
	for k := range f.tuples[storeID] {
		p := strings.Split(k, "|")
		if p[2] == body.TupleKey.Object {
			out.Tuples = append(out.Tuples, map[string]any{
				"key": map[string]string{"user": p[0], "relation": p[1], "object": p[2]},
			})
		}
	}
	writeJSON(w, out)
}

func newClient(t *testing.T) *fga.Client {
	t.Helper()
	srv := httptest.NewServer(newFakeFGA().handler())
	t.Cleanup(srv.Close)
	c, err := fga.New(context.Background(), fga.Config{APIURL: srv.URL, StoreName: "commerce", Model: testModel})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestCommerceModel_IsValidAndHasExpectedTypes(t *testing.T) {
	var m struct {
		SchemaVersion   string `json:"schema_version"`
		TypeDefinitions []struct {
			Type      string                     `json:"type"`
			Relations map[string]json.RawMessage `json:"relations"`
		} `json:"type_definitions"`
	}
	if err := json.Unmarshal([]byte(fga.CommerceModel), &m); err != nil {
		t.Fatalf("CommerceModel is not valid JSON: %v", err)
	}
	if m.SchemaVersion != "1.1" {
		t.Fatalf("schema_version = %q", m.SchemaVersion)
	}
	got := map[string][]string{}
	for _, td := range m.TypeDefinitions {
		var rels []string
		for r := range td.Relations {
			rels = append(rels, r)
		}
		got[td.Type] = rels
	}
	for _, typ := range []string{"user", "order", "shop", "product"} {
		if _, ok := got[typ]; !ok {
			t.Errorf("model is missing type %q", typ)
		}
	}
	if _, ok := got["order"]; !ok {
		t.Fatal("no order type")
	}
	has := func(typ string, rels ...string) {
		set := map[string]bool{}
		for _, r := range got[typ] {
			set[r] = true
		}
		for _, r := range rels {
			if !set[r] {
				t.Fatalf("%s relations = %v, want %v", typ, got[typ], rels)
			}
		}
	}
	has("shop", "owner", "staff")
	has("product", "shop", "manager")
}

func TestNew_BootstrapsStoreAndModel(t *testing.T) {
	c := newClient(t)
	if c.StoreID() == "" || c.ModelID() == "" {
		t.Fatalf("store/model not resolved: %q / %q", c.StoreID(), c.ModelID())
	}
}

func TestNew_ReusesExistingStore(t *testing.T) {
	srv := httptest.NewServer(newFakeFGA().handler())
	t.Cleanup(srv.Close)
	a, err := fga.New(context.Background(), fga.Config{APIURL: srv.URL, StoreName: "commerce", Model: testModel})
	if err != nil {
		t.Fatal(err)
	}
	b, err := fga.New(context.Background(), fga.Config{APIURL: srv.URL, StoreName: "commerce", Model: testModel})
	if err != nil {
		t.Fatal(err)
	}
	if a.StoreID() != b.StoreID() {
		t.Fatalf("second New created a new store: %q != %q", a.StoreID(), b.StoreID())
	}
}

func TestCheckWriteDeleteRoundTrip(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	ok, err := c.Check(ctx, "user:alice", "viewer", "order:1")
	if err != nil || ok {
		t.Fatalf("pre-grant Check = %v, %v; want false, nil", ok, err)
	}
	if err := c.Write(ctx, "user:alice", "viewer", "order:1"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	ok, err = c.Check(ctx, "user:alice", "viewer", "order:1")
	if err != nil || !ok {
		t.Fatalf("post-grant Check = %v, %v; want true, nil", ok, err)
	}

	// Re-writing the same tuple is an "already exists" error the caller can ignore.
	err = c.Write(ctx, "user:alice", "viewer", "order:1")
	if !fga.IsAlreadyExists(err) {
		t.Fatalf("duplicate Write err = %v; want IsAlreadyExists", err)
	}

	if err := c.Delete(ctx, "user:alice", "viewer", "order:1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := c.Delete(ctx, "user:alice", "viewer", "order:1"); !fga.IsNotFound(err) {
		t.Fatalf("re-Delete err = %v; want IsNotFound", err)
	}
	if ok, _ := c.Check(ctx, "user:alice", "viewer", "order:1"); ok {
		t.Fatal("Check still true after Delete")
	}
}

func TestListObjectsAndRead(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	for _, o := range []string{"order:1", "order:9"} {
		if err := c.Write(ctx, "user:bob", "viewer", o); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Write(ctx, "user:carol", "viewer", "order:1"); err != nil {
		t.Fatal(err)
	}

	objs, err := c.ListObjects(ctx, "user:bob", "viewer", "order")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sortedCopy(objs), ","); got != "order:1,order:9" {
		t.Fatalf("ListObjects = %q", got)
	}

	tuples, err := c.Read(ctx, "order:1")
	if err != nil {
		t.Fatal(err)
	}
	users := map[string]bool{}
	for _, tup := range tuples {
		users[tup.User] = true
	}
	if !users["user:bob"] || !users["user:carol"] {
		t.Fatalf("Read(order:1) users = %v", users)
	}
}

func TestNew_ErrorFromServerIsSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"code":"internal_error","message":"boom"}`))
	}))
	t.Cleanup(srv.Close)
	_, err := fga.New(context.Background(), fga.Config{APIURL: srv.URL, StoreName: "x", Model: testModel})
	var e *fga.Error
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want a surfaced *fga.Error, got %v", err)
	}
	_ = e
}

func TestNew_RejectsInvalidModelJSON(t *testing.T) {
	srv := httptest.NewServer(newFakeFGA().handler())
	t.Cleanup(srv.Close)
	_, err := fga.New(context.Background(), fga.Config{APIURL: srv.URL, StoreName: "commerce", Model: "{not json"})
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("want an invalid-JSON error, got %v", err)
	}
}

func TestNew_NoModelAndNoneSupplied(t *testing.T) {
	srv := httptest.NewServer(newFakeFGA().handler())
	t.Cleanup(srv.Close)
	_, err := fga.New(context.Background(), fga.Config{APIURL: srv.URL, StoreName: "commerce"})
	if err == nil || !strings.Contains(err.Error(), "no authorization model") {
		t.Fatalf("want a missing-model error, got %v", err)
	}
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
