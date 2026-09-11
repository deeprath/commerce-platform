import { render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { Order } from "../types";
import { OrderDetail, Orders } from "./Orders";

afterEach(() => {
  vi.restoreAllMocks();
});

const ORDER: Order = {
  id: "order_1234567890",
  status: "ORDER_STATUS_CONFIRMED",
  lines: [
    { product_id: "p1", title: "Trail Cap", quantity: 2, unit_price: { currency_code: "USD", units: "10", nanos: 0 }, line_total: { currency_code: "USD", units: "20", nanos: 0 } },
  ],
  subtotal: { currency_code: "USD", units: "20", nanos: 0 },
  discount: { currency_code: "USD", units: "0", nanos: 0 },
  tax: { currency_code: "USD", units: "2", nanos: 0 },
  total: { currency_code: "USD", units: "22", nanos: 0 },
  payment_id: "pay_1",
  cancel_reason: "",
  created_at: "2026-01-01T00:00:00Z",
};

function renderOrders(authed: boolean) {
  return render(
    <MemoryRouter>
      <Orders authed={authed} />
    </MemoryRouter>,
  );
}

function renderDetail(authed: boolean, path = "/orders/order_1234567890") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/orders/:id" element={<OrderDetail authed={authed} />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("Orders (list)", () => {
  it("prompts sign-in when not authed, without calling the API", () => {
    const listOrders = vi.spyOn(api, "listOrders");
    renderOrders(false);
    expect(screen.getByText(/sign in/i)).toBeInTheDocument();
    expect(listOrders).not.toHaveBeenCalled();
  });

  it("shows a loading state", () => {
    vi.spyOn(api, "listOrders").mockReturnValue(new Promise(() => {}));
    renderOrders(true);
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
  });

  it("shows an empty state", async () => {
    vi.spyOn(api, "listOrders").mockResolvedValue({ orders: [] });
    renderOrders(true);
    expect(await screen.findByText("You have no orders yet.")).toBeInTheDocument();
  });

  it("shows a load error", async () => {
    vi.spyOn(api, "listOrders").mockRejectedValue(new Error("network down"));
    renderOrders(true);
    expect(await screen.findByText(/network down/)).toBeInTheDocument();
  });

  it("lists orders with a truncated id, status, total, and a link", async () => {
    vi.spyOn(api, "listOrders").mockResolvedValue({ orders: [ORDER] });
    renderOrders(true);
    const link = await screen.findByRole("link", { name: /order_12/ });
    expect(link).toHaveAttribute("href", "/orders/order_1234567890");
    expect(within(link).getByText("Confirmed")).toBeInTheDocument();
    expect(within(link).getByText("$22.00")).toBeInTheDocument();
  });
});

describe("OrderDetail", () => {
  it("prompts sign-in when not authed, without calling the API", () => {
    const getOrder = vi.spyOn(api, "getOrder");
    renderDetail(false);
    expect(screen.getByText(/sign in/i)).toBeInTheDocument();
    expect(getOrder).not.toHaveBeenCalled();
  });

  it("shows a loading state", () => {
    vi.spyOn(api, "getOrder").mockReturnValue(new Promise(() => {}));
    renderDetail(true);
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
  });

  it("shows a not-found state", async () => {
    vi.spyOn(api, "getOrder").mockRejectedValue({ info: { reason: "ORDER_NOT_FOUND" } });
    renderDetail(true);
    expect(await screen.findByText("Order not found.")).toBeInTheDocument();
  });

  it("shows a load error", async () => {
    vi.spyOn(api, "getOrder").mockRejectedValue(new Error("network down"));
    renderDetail(true);
    expect(await screen.findByText(/network down/)).toBeInTheDocument();
  });

  it("fetches by the :id route param and renders the order", async () => {
    const getOrder = vi.spyOn(api, "getOrder").mockResolvedValue(ORDER);
    renderDetail(true);
    expect(await screen.findByRole("heading", { name: "Order #order_12" })).toBeInTheDocument();
    expect(getOrder).toHaveBeenCalledWith("order_1234567890");
    expect(screen.getByText("Confirmed")).toBeInTheDocument();
    expect(screen.getByText("Trail Cap")).toBeInTheDocument();
    expect(screen.getByText("2×")).toBeInTheDocument();
    expect(screen.getByText("Subtotal").nextElementSibling).toHaveTextContent("$20.00");
    expect(screen.getByText("Total").nextElementSibling).toHaveTextContent("$22.00");
    expect(screen.queryByText("Discount")).not.toBeInTheDocument();
  });

  it("shows the cancel reason when present", async () => {
    vi.spyOn(api, "getOrder").mockResolvedValue({ ...ORDER, status: "ORDER_STATUS_CANCELLED", cancel_reason: "PAYMENT_FAILED" });
    renderDetail(true);
    expect(await screen.findByText("Reason: PAYMENT_FAILED")).toBeInTheDocument();
  });

  it("shows a discount line when the order has one", async () => {
    vi.spyOn(api, "getOrder").mockResolvedValue({ ...ORDER, discount: { currency_code: "USD", units: "5", nanos: 0 } });
    renderDetail(true);
    await screen.findByRole("heading", { name: /Order #/ });
    expect(screen.getByText("Discount")).toBeInTheDocument();
    expect(screen.getByText("−$5.00")).toBeInTheDocument();
  });

  it("falls back to the product id when a line has no title", async () => {
    vi.spyOn(api, "getOrder").mockResolvedValue({ ...ORDER, lines: [{ ...ORDER.lines[0], title: "" }] });
    renderDetail(true);
    expect(await screen.findByText("p1")).toBeInTheDocument();
  });
});
