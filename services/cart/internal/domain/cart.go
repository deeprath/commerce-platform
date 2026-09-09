// Package domain is the cart aggregate and its mutation rules. No Redis or
// proto types here.
package domain

import (
	"sort"
	"time"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

// MaxLineQuantity caps a single line to keep a cart sane.
const MaxLineQuantity = 999

type Item struct {
	ProductID string    `json:"product_id"`
	Quantity  int32     `json:"quantity"`
	AddedAt   time.Time `json:"added_at"`
}

type Cart struct {
	ID        string    `json:"id"`
	Items     []Item    `json:"items"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewCart returns an empty cart with the given id.
func NewCart(id string) *Cart { return &Cart{ID: id, UpdatedAt: time.Now().UTC()} }

// TotalQuantity is the sum of all line quantities.
func (c *Cart) TotalQuantity() int32 {
	var n int32
	for _, it := range c.Items {
		n += it.Quantity
	}
	return n
}

func (c *Cart) find(productID string) *Item {
	for i := range c.Items {
		if c.Items[i].ProductID == productID {
			return &c.Items[i]
		}
	}
	return nil
}

// Add increases (or creates) a line by delta (> 0).
func (c *Cart) Add(productID string, delta int32) error {
	if productID == "" {
		return errs.New(errs.KindInvalidArgument, "PRODUCT_ID_REQUIRED", "product_id is required")
	}
	if delta <= 0 {
		return errs.New(errs.KindInvalidArgument, "BAD_QUANTITY", "quantity to add must be > 0")
	}
	if it := c.find(productID); it != nil {
		it.Quantity = clampQty(it.Quantity + delta)
	} else {
		c.Items = append(c.Items, Item{ProductID: productID, Quantity: clampQty(delta), AddedAt: time.Now().UTC()})
	}
	c.touch()
	return nil
}

// SetQuantity sets a line to exactly qty; qty == 0 removes it.
func (c *Cart) SetQuantity(productID string, qty int32) error {
	if qty < 0 {
		return errs.New(errs.KindInvalidArgument, "BAD_QUANTITY", "quantity must be >= 0")
	}
	if qty == 0 {
		c.Remove(productID)
		return nil
	}
	if it := c.find(productID); it != nil {
		it.Quantity = clampQty(qty)
	} else {
		c.Items = append(c.Items, Item{ProductID: productID, Quantity: clampQty(qty), AddedAt: time.Now().UTC()})
	}
	c.touch()
	return nil
}

// Remove deletes a line if present.
func (c *Cart) Remove(productID string) {
	out := c.Items[:0]
	for _, it := range c.Items {
		if it.ProductID != productID {
			out = append(out, it)
		}
	}
	c.Items = out
	c.touch()
}

// Clear empties the cart.
func (c *Cart) Clear() {
	c.Items = nil
	c.touch()
}

// MergeFrom folds another cart's lines into this one, summing quantities.
func (c *Cart) MergeFrom(other *Cart) {
	for _, oi := range other.Items {
		if it := c.find(oi.ProductID); it != nil {
			it.Quantity = clampQty(it.Quantity + oi.Quantity)
		} else {
			c.Items = append(c.Items, oi)
		}
	}
	c.touch()
}

func (c *Cart) touch() {
	c.UpdatedAt = time.Now().UTC()
	sort.Slice(c.Items, func(i, j int) bool { return c.Items[i].AddedAt.Before(c.Items[j].AddedAt) })
}

func clampQty(q int32) int32 {
	if q > MaxLineQuantity {
		return MaxLineQuantity
	}
	return q
}
