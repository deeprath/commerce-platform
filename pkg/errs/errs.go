// Package errs is the platform-wide error taxonomy. Services construct errors
// with these helpers; pkg/grpcx maps them onto gRPC statuses at the boundary,
// and the BFF maps those statuses onto HTTP responses (pkg/errs/http.go).
//
// The goal: one place that decides "this kind of failure => this gRPC code =>
// this HTTP status", so every service behaves identically to callers.
package errs

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Kind is a coarse failure category. It maps 1:1 to a gRPC code and an HTTP status.
type Kind int

const (
	KindInternal       Kind = iota // unexpected; a bug or a dependency failure
	KindInvalidArgument            // caller sent something malformed
	KindNotFound                   // the addressed resource does not exist / not visible
	KindAlreadyExists              // uniqueness violation
	KindPermissionDenied           // authenticated but not allowed
	KindUnauthenticated            // no / bad credentials
	KindFailedPrecondition        // resource state forbids the operation
	KindConflict                   // optimistic-concurrency / version clash
	KindResourceExhausted          // rate limited / quota / load-shed
	KindUnavailable                // dependency down; safe to retry
	KindDeadlineExceeded           // ran out of time
)

// Error is a domain error carrying a stable machine reason plus a Kind.
type Error struct {
	Kind    Kind
	Reason  string            // stable UPPER_SNAKE, e.g. "PRODUCT_NOT_FOUND"
	Message string            // human-readable, safe to log; not necessarily safe to return
	Meta    map[string]string // structured context; never PII/secrets
	cause   error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Reason, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Reason, e.Message)
}

func (e *Error) Unwrap() error { return e.cause }

// New builds an Error. cause may be nil.
func New(kind Kind, reason, message string) *Error {
	return &Error{Kind: kind, Reason: reason, Message: message}
}

// Wrap annotates an existing error with a Kind and reason.
func Wrap(cause error, kind Kind, reason, message string) *Error {
	return &Error{Kind: kind, Reason: reason, Message: message, cause: cause}
}

// WithMeta attaches a key/value pair and returns e for chaining.
func (e *Error) WithMeta(k, v string) *Error {
	if e.Meta == nil {
		e.Meta = map[string]string{}
	}
	e.Meta[k] = v
	return e
}

// GRPCCode returns the gRPC code for a Kind.
func (k Kind) GRPCCode() codes.Code {
	switch k {
	case KindInvalidArgument:
		return codes.InvalidArgument
	case KindNotFound:
		return codes.NotFound
	case KindAlreadyExists:
		return codes.AlreadyExists
	case KindPermissionDenied:
		return codes.PermissionDenied
	case KindUnauthenticated:
		return codes.Unauthenticated
	case KindFailedPrecondition:
		return codes.FailedPrecondition
	case KindConflict:
		return codes.Aborted
	case KindResourceExhausted:
		return codes.ResourceExhausted
	case KindUnavailable:
		return codes.Unavailable
	case KindDeadlineExceeded:
		return codes.DeadlineExceeded
	default:
		return codes.Internal
	}
}

// HTTPStatus returns the HTTP status for a Kind (used by the BFF).
func (k Kind) HTTPStatus() int {
	switch k {
	case KindInvalidArgument:
		return 400
	case KindUnauthenticated:
		return 401
	case KindPermissionDenied:
		return 403
	case KindNotFound:
		return 404
	case KindAlreadyExists, KindConflict:
		return 409
	case KindFailedPrecondition:
		return 412
	case KindResourceExhausted:
		return 429
	case KindUnavailable:
		return 503
	case KindDeadlineExceeded:
		return 504
	default:
		return 500
	}
}

// ToStatus converts any error to a *status.Status. A plain error becomes
// Internal with a generic message so implementation detail never leaks.
func ToStatus(err error) *status.Status {
	if err == nil {
		return status.New(codes.OK, "")
	}
	var de *Error
	if errors.As(err, &de) {
		st := status.New(de.Kind.GRPCCode(), de.Reason)
		return st
	}
	// Already a gRPC status? Pass it through.
	if st, ok := status.FromError(err); ok {
		return st
	}
	return status.New(codes.Internal, "INTERNAL")
}

// Is reports whether err is (or wraps) an *Error of the given Kind.
func Is(err error, kind Kind) bool {
	var de *Error
	return errors.As(err, &de) && de.Kind == kind
}
