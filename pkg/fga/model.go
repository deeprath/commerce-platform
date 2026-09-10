package fga

import (
	"context"
	_ "embed" // for the //go:embed of model.json
)

// StoreName is the single logical OpenFGA store the platform shares.
const StoreName = "commerce"

// CommerceModel is the canonical authorization model JSON, written to the store
// on first run. It is the union of every service's needs and grows additively
// as slices land:
//
//	type user
//	type order   { viewer: [user] }                       — delegated order sharing (ADR-035)
//	type shop    { owner: [user], staff: [user] or owner } — marketplace shop staff (ADR-039)
//
//go:embed model.json
var CommerceModel string

// Object-id builders for the `type:id` strings tuples use.
func UserObject(subject string) string { return "user:" + subject }
func OrderObject(id string) string     { return "order:" + id }
func ShopObject(id string) string      { return "shop:" + id }

// Relations.
const (
	RelationViewer = "viewer" // order#viewer
	RelationOwner  = "owner"  // shop#owner
	RelationStaff  = "staff"  // shop#staff (= [user] or owner)
)

// API is the subset of *Client the services depend on — an interface so gRPC
// layers can be tested without a live OpenFGA.
type API interface {
	Check(ctx context.Context, user, relation, object string) (bool, error)
	Write(ctx context.Context, user, relation, object string) error
	Delete(ctx context.Context, user, relation, object string) error
	Read(ctx context.Context, object string) ([]Tuple, error)
}
