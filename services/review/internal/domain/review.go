// Package domain is the review aggregate and its rules. No proto/SQL.
package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

type Status string

const (
	StatusPublished Status = "PUBLISHED"
	StatusHidden    Status = "HIDDEN"
)

const (
	MinRating     = 1
	MaxRating     = 5
	MaxTitleRunes = 120
	MaxBodyRunes  = 4000
)

type Review struct {
	ID               string
	ProductID        string
	AuthorID         string
	AuthorName       string
	Rating           int32
	Title            string
	Body             string
	Status           Status
	VerifiedPurchase bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Summary is the aggregate rating for a product.
type Summary struct {
	ProductID string
	Average   float64
	Count     int32
	// Histogram[i] is the count of published reviews with rating i+1.
	Histogram [5]int32
}

// NewReview validates the inputs and returns a PUBLISHED, verified review.
// title/body are trimmed and length-capped.
func NewReview(productID, authorID, authorName string, rating int32, title, body string) (*Review, error) {
	if productID == "" {
		return nil, errs.New(errs.KindInvalidArgument, "PRODUCT_ID_REQUIRED", "product_id is required")
	}
	if rating < MinRating || rating > MaxRating {
		return nil, errs.New(errs.KindInvalidArgument, "BAD_RATING", "rating must be between 1 and 5")
	}
	title = truncate(strings.TrimSpace(title), MaxTitleRunes)
	body = truncate(strings.TrimSpace(body), MaxBodyRunes)
	if body == "" {
		return nil, errs.New(errs.KindInvalidArgument, "BODY_REQUIRED", "review body is required")
	}
	name := strings.TrimSpace(authorName)
	if name == "" {
		name = "Customer"
	}
	return &Review{
		ProductID: productID, AuthorID: authorID, AuthorName: name,
		Rating: rating, Title: title, Body: body,
		Status: StatusPublished, VerifiedPurchase: true,
	}, nil
}

// ValidModerationTarget reports whether `to` is a status ModerateReview accepts.
func ValidModerationTarget(to Status) bool {
	return to == StatusPublished || to == StatusHidden
}

func truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}
