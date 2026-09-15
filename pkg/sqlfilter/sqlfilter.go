// Package sqlfilter builds a parameterised WHERE clause from only the
// predicates that actually apply.
//
// It replaces the empty-string-sentinel idiom that optional list filters reach
// for:
//
//	WHERE ($1 = '' OR owner_id = $1) AND ($2 = '' OR status = $2)
//
// That makes a single plan serve both the filtered and the unfiltered case, and
// Postgres can only prune the OR branch under a *custom* plan — the kind where
// it can see the parameter value. After the fifth execution of a prepared
// statement it may switch to a *generic* plan, where it cannot, and the same
// query flips from an index scan to a sequential scan under load with no code
// change and no warning. Which plan you get also depends on table statistics,
// so it can differ between environments.
//
// Emitting only the predicates that apply keeps the plan stable and lets a
// (col, created_at DESC) index actually be chosen for each shape of the query.
//
// Values never enter the SQL string — only placeholder numbers are formatted
// into it, and every value travels as a query argument.
package sqlfilter

import (
	"fmt"
	"strings"
)

// Filters accumulates WHERE predicates and their arguments in step.
//
// The zero value is ready to use:
//
//	var f sqlfilter.Filters
//	f.Add("created_at < $%d", cursor)
//	f.AddNonEmpty("owner_id = $%d", ownerID) // skipped when ownerID is ""
//	f.AddNonEmpty("status = $%d", status)
//	limit := f.Placeholder(pageSize + 1)
//	q := "SELECT id FROM orders" + f.Where() + " ORDER BY created_at DESC LIMIT " + limit
//	rows, err := pool.Query(ctx, q, f.Args()...)
type Filters struct {
	conds []string
	args  []any
}

// Add appends a predicate unconditionally. expr must contain one %d verb where
// the placeholder number goes, e.g. "created_at < $%d".
func (f *Filters) Add(expr string, arg any) {
	f.args = append(f.args, arg)
	f.conds = append(f.conds, fmt.Sprintf(expr, len(f.args)))
}

// AddNonEmpty is Add, skipped entirely when v is "" — the optional-filter case
// this package exists for. Skipping is the point: an absent predicate is not
// the same as one that matches everything.
func (f *Filters) AddNonEmpty(expr, v string) {
	if v != "" {
		f.Add(expr, v)
	}
}

// Placeholder registers a trailing argument that isn't a WHERE predicate —
// LIMIT, OFFSET — and returns its "$N" placeholder. Call it after every Add,
// so the numbering stays in order.
func (f *Filters) Placeholder(arg any) string {
	f.args = append(f.args, arg)
	return fmt.Sprintf("$%d", len(f.args))
}

// Where renders " WHERE a AND b", with a leading space so it concatenates
// directly onto a FROM clause. It returns "" when no predicate was added, so
// the caller doesn't have to special-case an empty filter set.
func (f *Filters) Where() string {
	if len(f.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(f.conds, " AND ")
}

// Args returns the arguments in placeholder order, for Query/Exec.
func (f *Filters) Args() []any { return f.args }
