// Package fga is a small client for an OpenFGA server — the platform's
// relationship-based (ReBAC / Zanzibar-style) authorization store. It is
// deliberately hand-rolled over net/http rather than pulling the full OpenFGA
// SDK into the shared pkg module: the surface the platform needs is Check /
// Write / Delete / Read / ListObjects plus a one-time store+model bootstrap.
//
// FGA is *additive*. Services keep their coarse checks (role gates, owner-scoped
// repositories); an FGA `Check` only ever grants extra access on top. An FGA
// outage therefore fails closed for delegated access and never blocks a
// resource owner.
package fga

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

var tracer = otel.Tracer("github.com/deeprath/commerce-platform/pkg/fga")

// Config configures a Client. Model is the OpenFGA authorization model as JSON
// (the `{"schema_version":"1.1","type_definitions":[...]}` shape) — the DSL is
// a client-side convenience the server never sees.
type Config struct {
	APIURL    string        // e.g. http://openfga:8080
	StoreName string        // logical store; created on first run
	Model     string        // authorization model JSON; written if the store has none
	Timeout   time.Duration // per-request; defaults to 5s
}

// Client talks to one OpenFGA store with one authorization model pinned.
type Client struct {
	http    *http.Client
	base    string
	storeID string
	modelID string
}

// Error is a non-2xx response from the OpenFGA API.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("openfga: %d %s: %s", e.Status, e.Code, e.Message)
}

// IsAlreadyExists reports whether err is a Write that lost to an identical
// existing tuple — safe to treat as success (the grant is in place).
func IsAlreadyExists(err error) bool {
	var e *Error
	return errors.As(err, &e) && strings.Contains(e.Message, "already exists")
}

// IsNotFound reports whether err is a Delete of a tuple that wasn't there —
// safe to treat as success (the grant is already gone).
func IsNotFound(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return strings.Contains(e.Message, "not found") || strings.Contains(e.Message, "did not find")
}

// New connects, ensures the named store exists, and pins its latest
// authorization model (writing Config.Model if the store has none).
//
// The store/model bootstrap is best-effort-idempotent and intended for the dev
// path; production provisions the store and model out of band and passes a
// pinned model id. Two replicas racing to create the same store is possible and
// harmless (a duplicate empty store); both then converge on a written model.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	c := &Client{
		http: &http.Client{Timeout: cfg.Timeout},
		base: strings.TrimRight(cfg.APIURL, "/"),
	}

	id, err := c.ensureStore(ctx, cfg.StoreName)
	if err != nil {
		return nil, err
	}
	c.storeID = id

	modelID, err := c.latestModelID(ctx)
	if err != nil {
		return nil, err
	}
	if modelID == "" {
		if cfg.Model == "" {
			return nil, fmt.Errorf("openfga store %q has no authorization model and none was supplied", cfg.StoreName)
		}
		modelID, err = c.writeModel(ctx, cfg.Model)
		if err != nil {
			return nil, err
		}
	}
	c.modelID = modelID
	return c, nil
}

// StoreID and ModelID expose what New resolved (useful for logging / tests).
func (c *Client) StoreID() string { return c.storeID }
func (c *Client) ModelID() string { return c.modelID }

// Check answers whether user has relation on object, e.g.
// Check(ctx, "user:alice", "viewer", "order:123").
func (c *Client) Check(ctx context.Context, user, relation, object string) (bool, error) {
	ctx, span := tracer.Start(ctx, "fga.Check")
	defer span.End()
	span.SetAttributes(
		attribute.String("fga.user", user),
		attribute.String("fga.relation", relation),
		attribute.String("fga.object", object),
	)
	var out struct {
		Allowed bool `json:"allowed"`
	}
	err := c.do(ctx, http.MethodPost, "/stores/"+c.storeID+"/check", map[string]any{
		"tuple_key":              tupleKey(user, relation, object),
		"authorization_model_id": c.modelID,
	}, &out)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return false, err
	}
	span.SetAttributes(attribute.Bool("fga.allowed", out.Allowed))
	return out.Allowed, nil
}

// Write adds one relationship tuple. A tuple that already exists is reported as
// an *Error for which IsAlreadyExists is true.
func (c *Client) Write(ctx context.Context, user, relation, object string) error {
	ctx, span := tracer.Start(ctx, "fga.Write")
	defer span.End()
	return c.do(ctx, http.MethodPost, "/stores/"+c.storeID+"/write", map[string]any{
		"writes":                 map[string]any{"tuple_keys": []any{tupleKey(user, relation, object)}},
		"authorization_model_id": c.modelID,
	}, nil)
}

// Delete removes one relationship tuple. A tuple that isn't there is reported as
// an *Error for which IsNotFound is true.
func (c *Client) Delete(ctx context.Context, user, relation, object string) error {
	ctx, span := tracer.Start(ctx, "fga.Delete")
	defer span.End()
	return c.do(ctx, http.MethodPost, "/stores/"+c.storeID+"/write", map[string]any{
		"deletes":                map[string]any{"tuple_keys": []any{tupleKey(user, relation, object)}},
		"authorization_model_id": c.modelID,
	}, nil)
}

// ListObjects returns the object ids of objectType on which user has relation,
// e.g. ListObjects(ctx, "user:alice", "viewer", "order") -> ["order:1","order:9"].
func (c *Client) ListObjects(ctx context.Context, user, relation, objectType string) ([]string, error) {
	ctx, span := tracer.Start(ctx, "fga.ListObjects")
	defer span.End()
	var out struct {
		Objects []string `json:"objects"`
	}
	err := c.do(ctx, http.MethodPost, "/stores/"+c.storeID+"/list-objects", map[string]any{
		"type":                   objectType,
		"relation":               relation,
		"user":                   user,
		"authorization_model_id": c.modelID,
	}, &out)
	return out.Objects, err
}

// Tuple is one stored relationship.
type Tuple struct {
	User     string
	Relation string
	Object   string
}

// Read returns the tuples on object (all relations), e.g. to enumerate who a
// resource is shared with.
func (c *Client) Read(ctx context.Context, object string) ([]Tuple, error) {
	ctx, span := tracer.Start(ctx, "fga.Read")
	defer span.End()
	var out struct {
		Tuples []struct {
			Key struct {
				User, Relation, Object string
			} `json:"key"`
		} `json:"tuples"`
	}
	err := c.do(ctx, http.MethodPost, "/stores/"+c.storeID+"/read", map[string]any{
		"tuple_key": map[string]any{"object": object},
	}, &out)
	if err != nil {
		return nil, err
	}
	ts := make([]Tuple, 0, len(out.Tuples))
	for _, t := range out.Tuples {
		ts = append(ts, Tuple{User: t.Key.User, Relation: t.Key.Relation, Object: t.Key.Object})
	}
	return ts, nil
}

// --- bootstrap helpers ---

func (c *Client) ensureStore(ctx context.Context, name string) (string, error) {
	var list struct {
		Stores []struct{ Id, Name string } `json:"stores"`
	}
	if err := c.do(ctx, http.MethodGet, "/stores", nil, &list); err != nil {
		return "", err
	}
	for _, s := range list.Stores {
		if s.Name == name {
			return s.Id, nil
		}
	}
	var created struct{ Id string }
	if err := c.do(ctx, http.MethodPost, "/stores", map[string]any{"name": name}, &created); err != nil {
		return "", err
	}
	return created.Id, nil
}

func (c *Client) latestModelID(ctx context.Context) (string, error) {
	var out struct {
		AuthorizationModels []struct{ Id string } `json:"authorization_models"`
	}
	if err := c.do(ctx, http.MethodGet, "/stores/"+c.storeID+"/authorization-models?page_size=1", nil, &out); err != nil {
		return "", err
	}
	if len(out.AuthorizationModels) == 0 {
		return "", nil
	}
	return out.AuthorizationModels[0].Id, nil
}

func (c *Client) writeModel(ctx context.Context, modelJSON string) (string, error) {
	var body map[string]any
	if err := json.Unmarshal([]byte(modelJSON), &body); err != nil {
		return "", fmt.Errorf("authorization model is not valid JSON: %w", err)
	}
	var out struct {
		AuthorizationModelId string `json:"authorization_model_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/stores/"+c.storeID+"/authorization-models", body, &out); err != nil {
		return "", err
	}
	return out.AuthorizationModelId, nil
}

// --- transport ---

func tupleKey(user, relation, object string) map[string]any {
	return map[string]any{"user": user, "relation": relation, "object": object}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		e := &Error{Status: resp.StatusCode, Message: string(raw)}
		var parsed struct{ Code, Message string }
		if json.Unmarshal(raw, &parsed) == nil && parsed.Message != "" {
			e.Code, e.Message = parsed.Code, parsed.Message
		}
		return e
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}
