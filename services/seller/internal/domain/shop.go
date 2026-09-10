// Package domain is the shop aggregate and its rules. No proto / no SQL.
package domain

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

type Status string

const (
	StatusPendingReview Status = "PENDING_REVIEW"
	StatusActive        Status = "ACTIVE"
	StatusSuspended     Status = "SUSPENDED"
)

const (
	MinNameRunes   = 2
	MaxNameRunes   = 80
	MaxDescRunes   = 2000
	MaxEmailRunes  = 254
	MaxReasonRunes = 500
	MinSlugLen     = 3
	MaxSlugLen     = 60
)

// Shop is a marketplace seller's storefront identity.
type Shop struct {
	ID               string
	OwnerID          string
	Name             string
	Slug             string
	Description      string
	ContactEmail     string
	Status           Status
	SuspensionReason string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

var (
	slugStrip   = regexp.MustCompile(`[^a-z0-9]+`)
	slugTrim    = regexp.MustCompile(`^-+|-+$`)
	emailShaped = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// NewShop validates the inputs and returns a PENDING_REVIEW shop with a slug
// derived from the name. The caller supplies the owner (Keycloak sub).
func NewShop(ownerID, name, description, contactEmail string) (*Shop, error) {
	if ownerID == "" {
		return nil, errs.New(errs.KindUnauthenticated, "OWNER_REQUIRED", "sign-in required")
	}
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < MinNameRunes || n > MaxNameRunes {
		return nil, errs.New(errs.KindInvalidArgument, "BAD_NAME", "shop name must be 2–80 characters")
	}
	slug := Slugify(name)
	if len(slug) < MinSlugLen {
		return nil, errs.New(errs.KindInvalidArgument, "BAD_NAME",
			"shop name must contain at least a few letters or digits")
	}
	description = truncate(strings.TrimSpace(description), MaxDescRunes)
	contactEmail = strings.TrimSpace(contactEmail)
	if contactEmail != "" && (utf8.RuneCountInString(contactEmail) > MaxEmailRunes || !emailShaped.MatchString(contactEmail)) {
		return nil, errs.New(errs.KindInvalidArgument, "BAD_EMAIL", "contact_email is not a valid address")
	}
	return &Shop{
		OwnerID: ownerID, Name: name, Slug: slug,
		Description: description, ContactEmail: contactEmail,
		Status: StatusPendingReview,
	}, nil
}

// ApplyUpdate mutates the display fields in place (owner-authorised upstream).
// The slug is stable once created — renaming a shop does not move its URL.
func (s *Shop) ApplyUpdate(name, description, contactEmail string) error {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < MinNameRunes || n > MaxNameRunes {
		return errs.New(errs.KindInvalidArgument, "BAD_NAME", "shop name must be 2–80 characters")
	}
	contactEmail = strings.TrimSpace(contactEmail)
	if contactEmail != "" && (utf8.RuneCountInString(contactEmail) > MaxEmailRunes || !emailShaped.MatchString(contactEmail)) {
		return errs.New(errs.KindInvalidArgument, "BAD_EMAIL", "contact_email is not a valid address")
	}
	s.Name = name
	s.Description = truncate(strings.TrimSpace(description), MaxDescRunes)
	s.ContactEmail = contactEmail
	return nil
}

// Activate moves the shop to ACTIVE from PENDING_REVIEW or SUSPENDED. Returns
// (changed, error). A no-op when already ACTIVE.
func (s *Shop) Activate() (bool, error) {
	switch s.Status {
	case StatusActive:
		return false, nil
	case StatusPendingReview, StatusSuspended:
		s.Status = StatusActive
		s.SuspensionReason = ""
		return true, nil
	default:
		return false, errs.New(errs.KindFailedPrecondition, "BAD_STATE",
			"shop cannot be activated from "+string(s.Status))
	}
}

// Suspend moves an ACTIVE (or PENDING_REVIEW — a rejection) shop to SUSPENDED.
// Returns (changed, error). A no-op when already SUSPENDED.
func (s *Shop) Suspend(reason string) (bool, error) {
	reason = truncate(strings.TrimSpace(reason), MaxReasonRunes)
	if reason == "" {
		return false, errs.New(errs.KindInvalidArgument, "REASON_REQUIRED", "a suspension reason is required")
	}
	switch s.Status {
	case StatusSuspended:
		return false, nil
	case StatusActive, StatusPendingReview:
		s.Status = StatusSuspended
		s.SuspensionReason = reason
		return true, nil
	default:
		return false, errs.New(errs.KindFailedPrecondition, "BAD_STATE",
			"shop cannot be suspended from "+string(s.Status))
	}
}

// Slugify turns a display name into a URL-safe slug (lowercase, ascii, hyphen
// separated), capped at MaxSlugLen.
func Slugify(name string) string {
	s := slugStrip.ReplaceAllString(strings.ToLower(name), "-")
	s = slugTrim.ReplaceAllString(s, "")
	if len(s) > MaxSlugLen {
		s = strings.Trim(s[:MaxSlugLen], "-")
	}
	return s
}

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}
