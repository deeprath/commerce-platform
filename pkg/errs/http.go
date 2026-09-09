package errs

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// HTTPError is the JSON body the BFF returns for a failed call.
type HTTPError struct {
	Status int    `json:"-"`
	Code   string `json:"code"`   // the gRPC code name, e.g. "NOT_FOUND"
	Reason string `json:"reason"` // stable machine reason from the service
}

// FromGRPC maps a downstream gRPC error onto an HTTPError. It never leaks
// internal detail: for Internal/Unknown it returns a generic reason.
func FromGRPC(err error) HTTPError {
	st, ok := status.FromError(err)
	if !ok || st == nil {
		return HTTPError{Status: 500, Code: "INTERNAL", Reason: "INTERNAL"}
	}
	h := HTTPError{Status: httpForCode(st.Code()), Code: st.Code().String(), Reason: st.Message()}
	if st.Code() == codes.Internal || st.Code() == codes.Unknown || st.Code() == codes.DataLoss {
		h.Reason = "INTERNAL"
	}
	return h
}

func httpForCode(c codes.Code) int {
	switch c {
	case codes.OK:
		return 200
	case codes.InvalidArgument, codes.OutOfRange:
		return 400
	case codes.Unauthenticated:
		return 401
	case codes.PermissionDenied:
		return 403
	case codes.NotFound:
		return 404
	case codes.AlreadyExists, codes.Aborted:
		return 409
	case codes.FailedPrecondition:
		return 412
	case codes.ResourceExhausted:
		return 429
	case codes.Unavailable:
		return 503
	case codes.DeadlineExceeded:
		return 504
	case codes.Unimplemented:
		return 501
	default:
		return 500
	}
}
