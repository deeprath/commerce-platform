// Package domain holds the catalog's business types and rules. It knows nothing
// about protobuf or SQL — adapters in internal/grpcsvc and internal/store map to
// and from these.
package domain

import (
	"strings"
	"time"
	"unicode"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

type Status string

const (
	StatusDraft    Status = "DRAFT"
	StatusActive   Status = "ACTIVE"
	StatusArchived Status = "ARCHIVED"
)

// Money is a minor-unit-safe amount (see commerce.common.v1.Money).
type Money struct {
	CurrencyCode string
	Units        int64
	Nanos        int32
}

// Product is the catalog aggregate.
type Product struct {
	ID          string
	Slug        string
	Title       string
	Description string
	CategoryID  string
	ListPrice   Money
	MediaKeys   []string
	Status      Status
	Attributes  map[string]string
	ShopID      string // owning marketplace shop; "" => first-party
	CreatedBy   string // Keycloak sub of the creator
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewProductInput is the create payload for NewProduct.
type NewProductInput struct {
	Slug        string
	Title       string
	Description string
	CategoryID  string
	Price       Money
	MediaKeys   []string
	Attributes  map[string]string
	ShopID      string // "" => first-party (platform-owned)
	CreatedBy   string // Keycloak sub of the creator
}

// NewProduct validates inputs and returns a DRAFT product (id/timestamps set by the store).
func NewProduct(in NewProductInput) (*Product, error) {
	slug := normaliseSlug(in.Slug)
	if err := validateSlug(slug); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, errs.New(errs.KindInvalidArgument, "TITLE_REQUIRED", "title must not be empty")
	}
	if err := validateMoney(in.Price); err != nil {
		return nil, err
	}
	attrs := in.Attributes
	if attrs == nil {
		attrs = map[string]string{}
	}
	return &Product{
		Slug:        slug,
		Title:       strings.TrimSpace(in.Title),
		Description: in.Description,
		CategoryID:  in.CategoryID,
		ListPrice:   in.Price,
		MediaKeys:   in.MediaKeys,
		Status:      StatusDraft,
		Attributes:  attrs,
		ShopID:      strings.TrimSpace(in.ShopID),
		CreatedBy:   in.CreatedBy,
	}, nil
}

// ApplyUpdate mutates p with the given fields after validating them.
func (p *Product) ApplyUpdate(title, description, categoryID string, price Money, mediaKeys []string, attrs map[string]string, status Status) error {
	if strings.TrimSpace(title) == "" {
		return errs.New(errs.KindInvalidArgument, "TITLE_REQUIRED", "title must not be empty")
	}
	if err := validateMoney(price); err != nil {
		return err
	}
	if status != StatusDraft && status != StatusActive {
		return errs.New(errs.KindInvalidArgument, "BAD_STATUS", "status may only be set to DRAFT or ACTIVE via update")
	}
	if p.Status == StatusArchived {
		return errs.New(errs.KindFailedPrecondition, "PRODUCT_ARCHIVED", "archived products cannot be updated")
	}
	p.Title = strings.TrimSpace(title)
	p.Description = description
	p.CategoryID = categoryID
	p.ListPrice = price
	p.MediaKeys = mediaKeys
	if attrs != nil {
		p.Attributes = attrs
	}
	p.Status = status
	return nil
}

// Archive is idempotent.
func (p *Product) Archive() { p.Status = StatusArchived }

func normaliseSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.ReplaceAll(s, " ", "-")
}

func validateSlug(s string) error {
	if len(s) < 3 || len(s) > 120 {
		return errs.New(errs.KindInvalidArgument, "SLUG_LENGTH", "slug must be 3–120 chars")
	}
	for _, r := range s {
		if !unicode.IsLower(r) && !unicode.IsDigit(r) && r != '-' {
			return errs.New(errs.KindInvalidArgument, "SLUG_CHARS", "slug may contain only a–z, 0–9 and hyphens")
		}
	}
	return nil
}

func validateMoney(m Money) error {
	if len(m.CurrencyCode) != 3 {
		return errs.New(errs.KindInvalidArgument, "BAD_CURRENCY", "currency_code must be a 3-letter ISO 4217 code")
	}
	if m.Units < 0 || m.Nanos < 0 {
		return errs.New(errs.KindInvalidArgument, "NEGATIVE_PRICE", "list price must not be negative")
	}
	if m.Nanos > 999_999_999 {
		return errs.New(errs.KindInvalidArgument, "BAD_NANOS", "nanos must be < 1e9")
	}
	return nil
}
