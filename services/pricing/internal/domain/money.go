// Package domain holds pricing math and rules, free of proto/SQL types.
package domain

// Money is a currency amount in minor units (cents for USD). All arithmetic is
// integer; conversion to/from the wire Money (units + nanos) happens at the edge.
type Money struct {
	Currency string
	Cents    int64
}

// FromUnitsNanos converts a wire Money into minor units. nanos are 10^-9, so
// one cent is 10^7 nanos.
func FromUnitsNanos(currency string, units int64, nanos int32) Money {
	return Money{Currency: currency, Cents: units*100 + int64(nanos)/10_000_000}
}

// UnitsNanos splits minor units back into (units, nanos) for the wire.
func (m Money) UnitsNanos() (int64, int32) {
	return m.Cents / 100, int32(m.Cents%100) * 10_000_000
}

func (m Money) MulQty(q int32) Money { return Money{m.Currency, m.Cents * int64(q)} }

func (m Money) Add(o Money) Money { return Money{m.Currency, m.Cents + o.Cents} }

func (m Money) Sub(o Money) Money {
	v := m.Cents - o.Cents
	if v < 0 {
		v = 0
	}
	return Money{m.Currency, v}
}

// PercentOff returns p% of m, rounded to the nearest cent (half up).
func (m Money) PercentOff(p int32) Money {
	if p <= 0 {
		return Money{m.Currency, 0}
	}
	if p > 100 {
		p = 100
	}
	return Money{m.Currency, (m.Cents*int64(p) + 50) / 100}
}

// TaxBps applies a tax rate in basis points (100 bps = 1%), rounded half up.
func (m Money) TaxBps(bps int32) Money {
	if bps <= 0 {
		return Money{m.Currency, 0}
	}
	return Money{m.Currency, (m.Cents*int64(bps) + 5000) / 10_000}
}

// Min returns the smaller of two amounts (used to cap a fixed discount at subtotal).
func Min(a, b Money) Money {
	if a.Cents < b.Cents {
		return a
	}
	return b
}
