import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { CartCtx } from "../cart-context";
import { useCart } from "../cart-context";
import * as trackModule from "../track";
import type { CartView, Order } from "../types";
import { Checkout } from "./Checkout";

vi.mock("../cart-context", () => ({ useCart: vi.fn() }));
vi.mock("../track", () => ({ track: vi.fn(), toMinor: () => 0 }));

const navigate = vi.fn();
vi.mock("react-router-dom", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-router-dom")>();
  return { ...actual, useNavigate: () => navigate };
});

const mockUseCart = vi.mocked(useCart);

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

const ORDER_LINES_COMMON = {
  lines: [],
  subtotal: CART.subtotal!,
  discount: CART.discount!,
  tax: CART.tax!,
  total: CART.total!,
  payment_id: "pay_1",
  cancel_reason: "",
  created_at: "2026-01-01T00:00:00Z",
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

function renderPage(authed = true) {
  return render(
    <MemoryRouter>
      <Checkout authed={authed} />
    </MemoryRouter>,
  );
}

async function fillRequiredFields(user: ReturnType<typeof userEvent.setup>) {
  await user.clear(screen.getByLabelText("Full name"));
  await user.type(screen.getByLabelText("Full name"), "Ada Lovelace");
  await user.clear(screen.getByLabelText("Address line 1"));
  await user.type(screen.getByLabelText("Address line 1"), "10 Analytical Ave");
  await user.clear(screen.getByLabelText("City"));
  await user.type(screen.getByLabelText("City"), "Springfield");
  await user.clear(screen.getByLabelText("State"));
  await user.type(screen.getByLabelText("State"), "IL");
  await user.clear(screen.getByLabelText("ZIP"));
  await user.type(screen.getByLabelText("ZIP"), "62701");
  await user.clear(screen.getByLabelText("Country (ISO-2)"));
  await user.type(screen.getByLabelText("Country (ISO-2)"), "US");
}

beforeEach(() => {
  navigate.mockClear();
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("Checkout", () => {
  it("prompts sign-in when not authed", () => {
    mockUseCart.mockReturnValue(cartCtx());
    renderPage(false);
    expect(screen.getByText(/sign in/i)).toBeInTheDocument();
  });

  it("shows a loading state while the cart is still loading", () => {
    mockUseCart.mockReturnValue(cartCtx({ cart: null, loading: true }));
    renderPage();
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
  });

  it("shows an empty-cart message with no items", () => {
    mockUseCart.mockReturnValue(cartCtx({ cart: { ...CART, items: [] }, loading: false }));
    renderPage();
    expect(screen.getByText("Your cart is empty.")).toBeInTheDocument();
  });

  it("fires begin_checkout once for a non-empty cart", () => {
    mockUseCart.mockReturnValue(cartCtx());
    renderPage();
    expect(trackModule.track).toHaveBeenCalledWith("begin_checkout", expect.objectContaining({ value_minor: 0 }));
    expect(trackModule.track).toHaveBeenCalledTimes(1);
  });

  it("renders the order summary with line items and totals", () => {
    mockUseCart.mockReturnValue(cartCtx());
    renderPage();
    const summary = screen.getByText("Order summary").closest("aside")!;
    expect(within(summary).getByText(/2× Trail Cap/)).toBeInTheDocument();
    expect(within(summary).getByText("Subtotal").nextElementSibling).toHaveTextContent("$20.00");
    expect(within(summary).getByText("Tax").nextElementSibling).toHaveTextContent("$2.00");
    expect(within(summary).getByText("Total").nextElementSibling).toHaveTextContent("$22.00");
    // A zero discount isn't shown as a line.
    expect(within(summary).queryByText("Discount")).not.toBeInTheDocument();
  });

  it("shows a discount line when the cart has one", () => {
    mockUseCart.mockReturnValue(cartCtx({ cart: { ...CART, discount: { currency_code: "USD", units: "5", nanos: 0 } } }));
    renderPage();
    expect(screen.getByText("Discount")).toBeInTheDocument();
    expect(screen.getByText("−$5.00")).toBeInTheDocument();
  });

  it("places an order and shows the pending-payment step", async () => {
    const user = userEvent.setup();
    mockUseCart.mockReturnValue(cartCtx());
    const checkout = vi
      .spyOn(api, "checkout")
      .mockResolvedValue({ order_id: "order_1", status: "ORDER_STATUS_PENDING_PAYMENT", payment_id: "pay_1", payment_client_secret: "", total: CART.total! });
    renderPage();

    await fillRequiredFields(user);
    await user.type(screen.getByPlaceholderText(/coupon code/i), "SAVE10");
    await user.click(screen.getByRole("button", { name: /place order/i }));

    await waitFor(() =>
      expect(checkout).toHaveBeenCalledWith(
        expect.objectContaining({
          ship_to: expect.objectContaining({ full_name: "Ada Lovelace", city: "Springfield" }),
          coupon_code: "SAVE10",
          currency_code: "USD",
          payment_method_token: "pm_card_ok",
        }),
      ),
    );
    expect(await screen.findByText("Complete payment")).toBeInTheDocument();
    expect(screen.getByText(/order_1/)).toBeInTheDocument();
  });

  it("omits coupon_code entirely when the coupon field is blank", async () => {
    const user = userEvent.setup();
    mockUseCart.mockReturnValue(cartCtx());
    const checkout = vi
      .spyOn(api, "checkout")
      .mockResolvedValue({ order_id: "order_1", status: "ORDER_STATUS_PENDING_PAYMENT", payment_id: "pay_1", payment_client_secret: "", total: CART.total! });
    renderPage();

    await fillRequiredFields(user);
    await user.click(screen.getByRole("button", { name: /place order/i }));

    await waitFor(() => expect(checkout).toHaveBeenCalled());
    expect(checkout.mock.calls[0][0].coupon_code).toBeUndefined();
  });

  it("shows the creating-order transient before paymentId arrives", async () => {
    const user = userEvent.setup();
    mockUseCart.mockReturnValue(cartCtx());
    let resolveCheckout!: (v: Awaited<ReturnType<typeof api.checkout>>) => void;
    vi.spyOn(api, "checkout").mockReturnValue(
      new Promise((resolve) => {
        resolveCheckout = resolve;
      }),
    );
    renderPage();

    await fillRequiredFields(user);
    await user.click(screen.getByRole("button", { name: /place order/i }));

    expect(await screen.findByText("Creating your order…")).toBeInTheDocument();
    resolveCheckout({ order_id: "order_1", status: "ORDER_STATUS_PENDING_PAYMENT", payment_id: "pay_1", payment_client_secret: "", total: CART.total! });
    expect(await screen.findByText("Complete payment")).toBeInTheDocument();
  });

  it("shows an error step when placing the order fails", async () => {
    const user = userEvent.setup();
    mockUseCart.mockReturnValue(cartCtx());
    vi.spyOn(api, "checkout").mockRejectedValue({ info: { reason: "CART_EMPTY" } });
    renderPage();

    await fillRequiredFields(user);
    await user.click(screen.getByRole("button", { name: /place order/i }));

    expect(await screen.findByText(/Checkout error: CART_EMPTY/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(screen.getByRole("button", { name: /place order/i })).toBeInTheDocument();
  });

  async function reachPayingStep(user: ReturnType<typeof userEvent.setup>) {
    vi.spyOn(api, "checkout").mockResolvedValue({
      order_id: "order_1",
      status: "ORDER_STATUS_PENDING_PAYMENT",
      payment_id: "pay_1",
      payment_client_secret: "",
      total: CART.total!,
    });
    renderPage();
    await fillRequiredFields(user);
    await user.click(screen.getByRole("button", { name: /place order/i }));
    await screen.findByText("Complete payment");
  }

  it("authorizes payment and shows the confirmed order once it leaves pending", async () => {
    const user = userEvent.setup();
    const ctx = cartCtx();
    mockUseCart.mockReturnValue(ctx);
    await reachPayingStep(user);

    vi.spyOn(api, "confirmCheckout").mockResolvedValue({ status: "ok" });
    const order: Order = { id: "order_1", status: "ORDER_STATUS_CONFIRMED", ...ORDER_LINES_COMMON };
    vi.spyOn(api, "getOrder").mockResolvedValue(order);

    await user.click(screen.getByRole("button", { name: "Authorize payment" }));

    expect(await screen.findByText("Order confirmed 🎉")).toBeInTheDocument();
    expect(trackModule.track).toHaveBeenCalledWith("purchase", expect.objectContaining({ value_minor: 0 }));
    expect(ctx.refresh).toHaveBeenCalled();
  });

  it("shows the cancelled message when the order comes back cancelled", async () => {
    const user = userEvent.setup();
    mockUseCart.mockReturnValue(cartCtx());
    await reachPayingStep(user);

    vi.spyOn(api, "confirmCheckout").mockResolvedValue({ status: "ok" });
    const order: Order = { id: "order_1", status: "ORDER_STATUS_CANCELLED", ...ORDER_LINES_COMMON };
    vi.spyOn(api, "getOrder").mockResolvedValue(order);

    await user.click(screen.getByRole("button", { name: "Abandon" }));

    expect(await screen.findByText(/Payment didn.t go through/)).toBeInTheDocument();
  });

  it("falls back to the raw status for an unrecognized done state", async () => {
    const user = userEvent.setup();
    mockUseCart.mockReturnValue(cartCtx());
    await reachPayingStep(user);

    vi.spyOn(api, "confirmCheckout").mockResolvedValue({ status: "ok" });
    const order: Order = { id: "order_1", status: "ORDER_STATUS_FULFILLED", ...ORDER_LINES_COMMON };
    vi.spyOn(api, "getOrder").mockResolvedValue(order);

    await user.click(screen.getByRole("button", { name: "Authorize payment" }));

    expect(await screen.findByText("Order status: ORDER_STATUS_FULFILLED")).toBeInTheDocument();
  });

  it("gives up after 15 polls and shows the order as still pending", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ delay: null, advanceTimers: vi.advanceTimersByTime });
    mockUseCart.mockReturnValue(cartCtx());
    await reachPayingStep(user);

    vi.spyOn(api, "confirmCheckout").mockResolvedValue({ status: "ok" });
    const stillPending: Order = { id: "order_1", status: "ORDER_STATUS_PENDING_PAYMENT", ...ORDER_LINES_COMMON };
    const getOrder = vi.spyOn(api, "getOrder").mockResolvedValue(stillPending);

    await user.click(screen.getByRole("button", { name: "Authorize payment" }));
    await act(() => vi.runAllTimersAsync());

    expect(await screen.findByText("Order status: ORDER_STATUS_PENDING_PAYMENT")).toBeInTheDocument();
    expect(getOrder).toHaveBeenCalledTimes(15);
  });

  it("shows an error step when confirmation itself fails", async () => {
    const user = userEvent.setup();
    mockUseCart.mockReturnValue(cartCtx());
    await reachPayingStep(user);

    vi.spyOn(api, "confirmCheckout").mockRejectedValue({ info: { reason: "PSP_DOWN" } });

    await user.click(screen.getByRole("button", { name: "Authorize payment" }));

    expect(await screen.findByText(/Checkout error: PSP_DOWN/)).toBeInTheDocument();
  });

  it("navigates to the order page from the done step", async () => {
    const user = userEvent.setup();
    mockUseCart.mockReturnValue(cartCtx());
    await reachPayingStep(user);

    vi.spyOn(api, "confirmCheckout").mockResolvedValue({ status: "ok" });
    const order: Order = { id: "order_1", status: "ORDER_STATUS_CONFIRMED", ...ORDER_LINES_COMMON };
    vi.spyOn(api, "getOrder").mockResolvedValue(order);
    await user.click(screen.getByRole("button", { name: "Authorize payment" }));
    await screen.findByText("Order confirmed 🎉");

    await user.click(screen.getByRole("button", { name: "View order" }));
    expect(navigate).toHaveBeenCalledWith("/orders/order_1");
  });
});
