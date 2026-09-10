// Package authz holds the order service's OpenFGA authorization model and the
// naming helpers for its tuples. The actual client lives in pkg/fga.
package authz

import (
	"context"
	_ "embed" // powers the //go:embed of model.json below

	"github.com/deeprath/commerce-platform/pkg/fga"
)

// Model is the authorization model JSON handed to OpenFGA on first run.
//
//go:embed model.json
var Model string

// StoreName is the logical OpenFGA store the platform shares.
const StoreName = "commerce"

// RelationViewer: a user granted delegated read access to an order.
const RelationViewer = "viewer"

// UserObject / OrderObject build the `type:id` strings OpenFGA tuples use.
func UserObject(subject string) string { return "user:" + subject }
func OrderObject(id string) string     { return "order:" + id }

// Sharer is the slice of *fga.Client the order service uses. An interface so the
// gRPC layer can be tested without a live OpenFGA.
type Sharer interface {
	Check(ctx context.Context, user, relation, object string) (bool, error)
	Write(ctx context.Context, user, relation, object string) error
	Delete(ctx context.Context, user, relation, object string) error
	Read(ctx context.Context, object string) ([]fga.Tuple, error)
}
