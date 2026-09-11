import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { CartProvider } from "./cart";
import { useCart } from "./cart-context";
import * as trackModule from "./track";
import type { CartView } from "./types";

vi.mock("./track", () => ({ track: vi.fn(), toMinor: (m?: { units?: string }) => (m ? Number(m.units) * 100 : 0) }));

afterEach(() => {
  vi.restoreAllMocks();
});

const CART: CartView = {
  id: "c_1",
  items: [{ product_id: "p1", slug: "trail-cap", title: "Trail Cap", quantity: 2, primary_media_key: "" }],
  total_quantity: 2,
  total: { currency_code: "USD", units: "22", nanos: 0 },
};

// A minimal consumer that surfaces every CartCtx field/action through the DOM
// so tests can assert on them without reaching into React internals.
function Consumer() {
  const { cart, count, loading, refresh, add, setQty, remove, clear } = useCart();
  return (
    <div>
      <span data-testid="loading">{String(loading)}</span>
      <span data-testid="count">{count}</span>
      <span data-testid="items">{cart?.items.length ?? "none"}</span>
      <button onClick={() => void refresh("SAVE10")}>refresh</button>
      <button onClick={() => void add("p1", 2)}>add</button>
      <button onClick={() => void setQty("p1", 5)}>setQty</button>
      <button onClick={() => void remove("p1")}>remove</button>
      <button onClick={() => void clear()}>clear</button>
    </div>
  );
}

function renderProvider() {
  return render(
    <CartProvider>
      <Consumer />
    </CartProvider>,
  );
}

describe("CartProvider", () => {
  it("loads the cart on mount and flips loading false", async () => {
    const getCart = vi.spyOn(api, "getCart").mockResolvedValue(CART);
    renderProvider();
    expect(screen.getByTestId("loading")).toHaveTextContent("true");
    await waitFor(() => expect(screen.getByTestId("loading")).toHaveTextContent("false"));
    expect(getCart).toHaveBeenCalledWith(undefined);
    expect(screen.getByTestId("count")).toHaveTextContent("2");
    expect(screen.getByTestId("items")).toHaveTextContent("1");
  });

  it("keeps the last known cart (null) and still stops loading when getCart fails", async () => {
    vi.spyOn(api, "getCart").mockRejectedValue(new Error("down"));
    renderProvider();
    await waitFor(() => expect(screen.getByTestId("loading")).toHaveTextContent("false"));
    expect(screen.getByTestId("items")).toHaveTextContent("none");
    expect(screen.getByTestId("count")).toHaveTextContent("0");
  });

  it("refresh(coupon) re-fetches with the coupon code", async () => {
    const user = userEvent.setup();
    const getCart = vi.spyOn(api, "getCart").mockResolvedValue(CART);
    renderProvider();
    await waitFor(() => expect(screen.getByTestId("loading")).toHaveTextContent("false"));

    await user.click(screen.getByRole("button", { name: "refresh" }));
    await waitFor(() => expect(getCart).toHaveBeenLastCalledWith("SAVE10"));
  });

  it("add() calls the API, updates the cart, and tracks add_to_cart", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "getCart").mockResolvedValue({ ...CART, items: [], total_quantity: 0 });
    const updated = { ...CART, total_quantity: 4 };
    const addToCart = vi.spyOn(api, "addToCart").mockResolvedValue(updated);
    renderProvider();
    await waitFor(() => expect(screen.getByTestId("loading")).toHaveTextContent("false"));

    await user.click(screen.getByRole("button", { name: "add" }));

    expect(addToCart).toHaveBeenCalledWith("p1", 2);
    await waitFor(() => expect(screen.getByTestId("count")).toHaveTextContent("4"));
    expect(trackModule.track).toHaveBeenCalledWith("add_to_cart", { product_id: "p1", value_minor: 2200 });
  });

  it("setQty() calls the API and updates the cart", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "getCart").mockResolvedValue(CART);
    const setCartQuantity = vi.spyOn(api, "setCartQuantity").mockResolvedValue({ ...CART, total_quantity: 5 });
    renderProvider();
    await waitFor(() => expect(screen.getByTestId("loading")).toHaveTextContent("false"));

    await user.click(screen.getByRole("button", { name: "setQty" }));

    expect(setCartQuantity).toHaveBeenCalledWith("p1", 5);
    await waitFor(() => expect(screen.getByTestId("count")).toHaveTextContent("5"));
  });

  it("remove() calls the API and updates the cart", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "getCart").mockResolvedValue(CART);
    const removeFromCart = vi.spyOn(api, "removeFromCart").mockResolvedValue({ ...CART, items: [], total_quantity: 0 });
    renderProvider();
    await waitFor(() => expect(screen.getByTestId("loading")).toHaveTextContent("false"));

    await user.click(screen.getByRole("button", { name: "remove" }));

    expect(removeFromCart).toHaveBeenCalledWith("p1");
    await waitFor(() => expect(screen.getByTestId("items")).toHaveTextContent("0"));
  });

  it("clear() calls the API and updates the cart", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "getCart").mockResolvedValue(CART);
    const clearCart = vi.spyOn(api, "clearCart").mockResolvedValue({ ...CART, items: [], total_quantity: 0 });
    renderProvider();
    await waitFor(() => expect(screen.getByTestId("loading")).toHaveTextContent("false"));

    await user.click(screen.getByRole("button", { name: "clear" }));

    expect(clearCart).toHaveBeenCalled();
    await waitFor(() => expect(screen.getByTestId("count")).toHaveTextContent("0"));
  });
});
