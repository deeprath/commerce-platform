package sqlfilter_test

import (
	"testing"
	"time"

	"github.com/deeprath/commerce-platform/pkg/sqlfilter"
)

func TestFilters_OnlyAppliedPredicatesReachTheSQL(t *testing.T) {
	cursor := time.Now()
	var f sqlfilter.Filters
	f.Add("created_at < $%d", cursor)
	f.AddNonEmpty("owner_id = $%d", "owner-1")
	f.AddNonEmpty("status = $%d", "") // absent filter — must not appear at all
	limit := f.Placeholder(21)

	want := " WHERE created_at < $1 AND owner_id = $2"
	if got := f.Where(); got != want {
		t.Fatalf("Where() = %q, want %q", got, want)
	}
	if limit != "$3" {
		t.Fatalf("limit placeholder = %q, want $3", limit)
	}
	args := f.Args()
	if len(args) != 3 || args[0] != cursor || args[1] != "owner-1" || args[2] != 21 {
		t.Fatalf("Args() = %#v", args)
	}
}

// The admin path: no optional filter supplied at all. The unfiltered query must
// be genuinely unfiltered rather than carrying a match-everything predicate,
// which is the whole reason this package exists.
func TestFilters_AllOptionalFiltersAbsent(t *testing.T) {
	var f sqlfilter.Filters
	f.AddNonEmpty("owner_id = $%d", "")
	f.AddNonEmpty("status = $%d", "")

	if got := f.Where(); got != "" {
		t.Fatalf("Where() = %q, want empty for an unfiltered query", got)
	}
	if len(f.Args()) != 0 {
		t.Fatalf("Args() = %#v, want none", f.Args())
	}
}

// Placeholder numbering has to stay in step with Args order, or the query binds
// the wrong values — a silently wrong result rather than an error.
func TestFilters_NumberingFollowsArgOrder(t *testing.T) {
	var f sqlfilter.Filters
	f.AddNonEmpty("a = $%d", "first")
	f.Add("b < $%d", 2)
	p := f.Placeholder("third")

	if got := f.Where(); got != " WHERE a = $1 AND b < $2" {
		t.Fatalf("Where() = %q", got)
	}
	if p != "$3" {
		t.Fatalf("placeholder = %q, want $3", p)
	}
	args := f.Args()
	if args[0] != "first" || args[1] != 2 || args[2] != "third" {
		t.Fatalf("Args() out of order: %#v", args)
	}
}

func TestFilters_ZeroValueIsUsable(t *testing.T) {
	var f sqlfilter.Filters
	if got := f.Where(); got != "" {
		t.Fatalf("zero-value Where() = %q, want empty", got)
	}
	if f.Args() != nil && len(f.Args()) != 0 {
		t.Fatalf("zero-value Args() = %#v", f.Args())
	}
}
