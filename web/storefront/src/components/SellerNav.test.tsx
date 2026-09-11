import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { SellerNav } from "./SellerNav";

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <SellerNav />
    </MemoryRouter>,
  );
}

describe("SellerNav", () => {
  it("renders a link for every seller tab", () => {
    renderAt("/seller");
    for (const label of ["Shop", "Products", "Staff", "Payouts"]) {
      expect(screen.getByRole("link", { name: label })).toBeInTheDocument();
    }
  });

  it("marks only the exact /seller tab active on the index route (end: true)", () => {
    renderAt("/seller");
    expect(screen.getByRole("link", { name: "Shop" })).toHaveClass("active");
    expect(screen.getByRole("link", { name: "Products" })).not.toHaveClass("active");
  });

  it("marks the Products tab active on /seller/products", () => {
    renderAt("/seller/products");
    expect(screen.getByRole("link", { name: "Products" })).toHaveClass("active");
    // Shop is not `end`-exclusive by default in this list, but /seller isn't
    // a prefix of /seller/products's sibling tabs, so it stays inactive too.
    expect(screen.getByRole("link", { name: "Shop" })).not.toHaveClass("active");
  });
});
