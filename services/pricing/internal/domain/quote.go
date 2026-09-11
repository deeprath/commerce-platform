package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"time"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

type CouponKind string

const (
	CouponPercent CouponKind = "PERCENT"
	CouponAmount  CouponKind = "AMOUNT"
)

// Coupon is a discount rule.
type Coupon struct {
	Code       string
	Kind       CouponKind
	PercentOff int32
	AmountOff  Money
	ExpiresAt  time.Time
	MaxUses    int64
	UsedCount  int64
}

// Valid reports whether the coupon can be applied now, and why not otherwise.
func (c *Coupon) Valid(now time.Time) (bool, string) {
	if !c.ExpiresAt.IsZero() && now.After(c.ExpiresAt) {
		return false, "EXPIRED"
	}
	if c.MaxUses > 0 && c.UsedCount >= c.MaxUses {
		return false, "EXHAUSTED"
	}
	return true, ""
}

// Line is one priced cart line.
type Line struct {
	ProductID string
	Title     string
	Quantity  int32
	UnitPrice Money
	LineTotal Money
	// ShopID is the owning marketplace shop, copied from the catalog product.
	// Empty => a first-party line. Not part of the pricing math or the quote
	// signature (it's derived from ProductID, already covered there).
	ShopID string
}

// Quote is the fully priced result.
type Quote struct {
	Lines     []Line
	Subtotal  Money
	Discount  Money
	Tax       Money
	Total     Money
	Coupon    string
	Signature string
}

// Price builds a quote from priced lines, an optional coupon, and a tax rate.
// currency must be consistent across all inputs.
func Price(currency string, lines []Line, coupon *Coupon, taxBps int32, now time.Time) (*Quote, error) {
	if len(lines) == 0 {
		return nil, errs.New(errs.KindInvalidArgument, "EMPTY_QUOTE", "no lines to price")
	}
	subtotal := Money{Currency: currency}
	for i := range lines {
		if lines[i].Quantity <= 0 {
			return nil, errs.New(errs.KindInvalidArgument, "BAD_QUANTITY", "line quantity must be > 0")
		}
		lines[i].LineTotal = lines[i].UnitPrice.MulQty(lines[i].Quantity)
		subtotal = subtotal.Add(lines[i].LineTotal)
	}

	discount := Money{Currency: currency}
	appliedCoupon := ""
	if coupon != nil {
		if ok, reason := coupon.Valid(now); !ok {
			return nil, errs.New(errs.KindFailedPrecondition, "COUPON_"+reason, "coupon cannot be applied")
		}
		switch coupon.Kind {
		case CouponPercent:
			discount = subtotal.PercentOff(coupon.PercentOff)
		case CouponAmount:
			discount = Min(Money{currency, coupon.AmountOff.Cents}, subtotal)
		}
		appliedCoupon = coupon.Code
	}

	taxable := subtotal.Sub(discount)
	tax := taxable.TaxBps(taxBps)
	total := taxable.Add(tax)

	q := &Quote{
		Lines: lines, Subtotal: subtotal, Discount: discount, Tax: tax, Total: total,
		Coupon: appliedCoupon,
	}
	q.Signature = signature(currency, q)
	return q, nil
}

// signature is a stable hash of the priced inputs so a later re-quote can be
// compared for drift.
func signature(currency string, q *Quote) string {
	ls := make([]Line, len(q.Lines))
	copy(ls, q.Lines)
	sort.Slice(ls, func(i, j int) bool { return ls[i].ProductID < ls[j].ProductID })
	h := sha256.New()
	h.Write([]byte("cur=" + currency +
		";disc=" + strconv.FormatInt(q.Discount.Cents, 10) +
		";tax=" + strconv.FormatInt(q.Tax.Cents, 10) +
		";total=" + strconv.FormatInt(q.Total.Cents, 10) + ";"))
	for _, l := range ls {
		h.Write([]byte(l.ProductID + ":" +
			strconv.FormatInt(int64(l.Quantity), 10) + "@" +
			strconv.FormatInt(l.UnitPrice.Cents, 10) + ";"))
	}
	return hex.EncodeToString(h.Sum(nil))
}
