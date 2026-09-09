package domain

import (
	"testing"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

func TestAddAndSet(t *testing.T) {
	c := NewCart("c1")
	if err := c.Add("p1", 2); err != nil {
		t.Fatal(err)
	}
	if err := c.Add("p1", 3); err != nil {
		t.Fatal(err)
	}
	if c.find("p1").Quantity != 5 {
		t.Fatalf("qty = %d", c.find("p1").Quantity)
	}
	if err := c.Add("p1", 0); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("add 0 => %v", err)
	}

	if err := c.SetQuantity("p1", 1); err != nil {
		t.Fatal(err)
	}
	if c.find("p1").Quantity != 1 {
		t.Fatal("set failed")
	}
	if err := c.SetQuantity("p1", 0); err != nil {
		t.Fatal(err)
	}
	if c.find("p1") != nil {
		t.Fatal("qty 0 should remove the line")
	}
}

func TestClampAndTotals(t *testing.T) {
	c := NewCart("c1")
	_ = c.Add("p1", 5000)
	if c.find("p1").Quantity != MaxLineQuantity {
		t.Fatalf("not clamped: %d", c.find("p1").Quantity)
	}
	_ = c.Add("p2", 3)
	if c.TotalQuantity() != MaxLineQuantity+3 {
		t.Fatalf("total = %d", c.TotalQuantity())
	}
}

func TestMerge(t *testing.T) {
	guest := NewCart("g")
	_ = guest.Add("p1", 2)
	_ = guest.Add("p2", 1)

	user := NewCart("u")
	_ = user.Add("p1", 1)
	_ = user.Add("p3", 4)

	user.MergeFrom(guest)
	got := map[string]int32{}
	for _, it := range user.Items {
		got[it.ProductID] = it.Quantity
	}
	if got["p1"] != 3 || got["p2"] != 1 || got["p3"] != 4 {
		t.Fatalf("merged = %v", got)
	}
}
