import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { CartCtx } from "../cart-context";
import { useCart } from "../cart-context";
import type { CartView } from "../types";
import { Cart } from "./Cart";

vi.mock("../cart-context", () => ({ useCart: vi.fn() }));
const mockUseCart = vi.mocked(useCart);

afterEach(() => {
  vi.restoreAllMocks();
});

const CART: CartView = {
  id: "c_1",
  items: [
    {
      product_id: "p1",
      slug: "trail-cap",
      title: "Trail Cap",
      quantity: 2,
      unit_price: { currency_code: "USD", units: "10", nanos: 0 },
      line_total: { currency_code: "USD", units: "20", nanos: 0 },
      primary_media_key: "",
    },
  ],
  total_quantity: 2,
  subtotal: { currency_code: "USD", units: "20", nanos: 0 },
  discount: { currency_code: "USD", units: "0", nanos: 0 },
  tax: { currency_code: "USD", units: "2", nanos: 0 },
  total: { currency_code: "USD", units: "22", nanos: 0 },
};

function cartCtx(overrides: Partial<CartCtx> = {}): CartCtx {
  return {
    cart: CART,
    count: 2,
    loading: false,
    refresh: vi.fn().mockResolvedValue(undefined),
    add: vi.fn(),
    setQty: vi.fn(),
    remove: vi.fn(),
    clear: vi.fn(),
    ...overrides,
  };
}

function renderPage() {
  return render(
    <MemoryRouter>
      <Cart />
    </MemoryRouter>,
  );
}

describe("Cart", () => {
  it("shows a loading state", () => {
    mockUseCart.mockReturnValue(cartCtx({ loading: true, cart: null }));
    renderPage();
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
  });

  it("shows an empty-cart message with no items", () => {
    mockUseCart.mockReturnValue(cartCtx({ cart: { ...CART, items: [] } }));
    renderPage();
    expect(screen.getByText("Your cart is empty.")).toBeInTheDocument();
  });

  it("lists cart lines with price, quantity, and line total", () => {
    mockUseCart.mockReturnValue(cartCtx());
    renderPage();
    expect(screen.getByRole("link", { name: "Trail Cap" })).toHaveAttribute("href", "/p/trail-cap");
    expect(screen.getByText("$10.00 each")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
    expect(screen.getByText("Trail Cap").closest(".cart-line")).toHaveTextContent("$20.00");
    expect(screen.getByText("Subtotal").nextElementSibling).toHaveTextContent("$20.00");
  });

  it("falls back to the product id when a line has no title", () => {
    mockUseCart.mockReturnValue(cartCtx({ cart: { ...CART, items: [{ ...CART.items[0], title: "" }] } }));
    renderPage();
    expect(screen.getByRole("link", { name: "p1" })).toBeInTheDocument();
  });

  it("increases and decreases quantity", async () => {
    const user = userEvent.setup();
    const ctx = cartCtx();
    mockUseCart.mockReturnValue(ctx);
    renderPage();

    await user.click(screen.getByLabelText("Increase"));
    expect(ctx.setQty).toHaveBeenCalledWith("p1", 3);

    await user.click(screen.getByLabelText("Decrease"));
    expect(ctx.setQty).toHaveBeenCalledWith("p1", 1);
  });

  it("never decreases quantity below 0", async () => {
    const user = userEvent.setup();
    const ctx = cartCtx({ cart: { ...CART, items: [{ ...CART.items[0], quantity: 0 }] } });
    mockUseCart.mockReturnValue(ctx);
    renderPage();

    await user.click(screen.getByLabelText("Decrease"));
    expect(ctx.setQty).toHaveBeenCalledWith("p1", 0);
  });

  it("removes a line", async () => {
    const user = userEvent.setup();
    const ctx = cartCtx();
    mockUseCart.mockReturnValue(ctx);
    renderPage();

    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(ctx.remove).toHaveBeenCalledWith("p1");
  });

  it("applies a coupon via refresh", async () => {
    const user = userEvent.setup();
    const ctx = cartCtx();
    mockUseCart.mockReturnValue(ctx);
    renderPage();

    await user.type(screen.getByLabelText("Coupon code"), "SAVE10");
    await user.click(screen.getByRole("button", { name: "Apply" }));
    expect(ctx.refresh).toHaveBeenCalledWith("SAVE10");
  });

  it("shows a coupon error", () => {
    mockUseCart.mockReturnValue(cartCtx({ cart: { ...CART, coupon_error: "EXPIRED" } }));
    renderPage();
    expect(screen.getByText("Coupon: EXPIRED")).toBeInTheDocument();
  });

  it("shows the discount line with the coupon code when there is one", () => {
    mockUseCart.mockReturnValue(
      cartCtx({ cart: { ...CART, discount: { currency_code: "USD", units: "5", nanos: 0 }, coupon_code: "SAVE10" } }),
    );
    renderPage();
    expect(screen.getByText("Discount (SAVE10)")).toBeInTheDocument();
    expect(screen.getByText("−$5.00")).toBeInTheDocument();
  });

  it("links to checkout", () => {
    mockUseCart.mockReturnValue(cartCtx());
    renderPage();
    expect(screen.getByRole("link", { name: "Checkout" })).toHaveAttribute("href", "/checkout");
  });
});
