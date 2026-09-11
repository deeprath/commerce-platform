import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { Order } from "../types";
import { OrderDetail } from "./OrderDetail";

afterEach(() => {
  vi.restoreAllMocks();
});

const ORDER: Order = {
  id: "order_1234567890",
  owner_id: "owner_abcdefgh",
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
  updated_at: "2026-01-01T00:00:00Z",
};

function renderPage(path = "/orders/order_1234567890") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/orders/:id" element={<OrderDetail />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("OrderDetail (admin)", () => {
  it("shows a loading state", () => {
    vi.spyOn(api, "getOrder").mockReturnValue(new Promise(() => {}));
    renderPage();
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
  });

  it("shows a load error", async () => {
    vi.spyOn(api, "getOrder").mockRejectedValue(new Error("network down"));
    renderPage();
    expect(await screen.findByText("network down")).toBeInTheDocument();
  });

  it("fetches by the :id route param and renders the order", async () => {
    const getOrder = vi.spyOn(api, "getOrder").mockResolvedValue(ORDER);
    renderPage();
    expect(await screen.findByText(/order_12/)).toBeInTheDocument();
    expect(getOrder).toHaveBeenCalledWith("order_1234567890");
    expect(screen.getByText("Confirmed")).toBeInTheDocument();
    expect(screen.getByText("owner_abcdefgh")).toBeInTheDocument();
    expect(screen.getByText("pay_1")).toBeInTheDocument();
    expect(screen.getByText("Trail Cap")).toBeInTheDocument();
    expect(screen.getByText("Subtotal").nextElementSibling).toHaveTextContent("$20.00");
    expect(screen.getByText("Total").nextElementSibling).toHaveTextContent("$22.00");
    expect(screen.queryByText("Discount")).not.toBeInTheDocument();
    expect(screen.queryByText("Cancel reason")).not.toBeInTheDocument();
  });

  it("shows an em dash when there's no payment id", async () => {
    vi.spyOn(api, "getOrder").mockResolvedValue({ ...ORDER, payment_id: "" });
    renderPage();
    await screen.findByText(/order_12/);
    expect(screen.getByText("—")).toBeInTheDocument();
  });

  it("shows the cancel reason when present", async () => {
    vi.spyOn(api, "getOrder").mockResolvedValue({ ...ORDER, cancel_reason: "PAYMENT_FAILED" });
    renderPage();
    expect(await screen.findByText("PAYMENT_FAILED")).toBeInTheDocument();
  });

  it("shows a discount line when the order has one", async () => {
    vi.spyOn(api, "getOrder").mockResolvedValue({ ...ORDER, discount: { currency_code: "USD", units: "5", nanos: 0 } });
    renderPage();
    await screen.findByText(/order_12/);
    expect(screen.getByText("Discount")).toBeInTheDocument();
    expect(screen.getByText("−$5.00")).toBeInTheDocument();
  });

  it("falls back to the product id when a line has no title", async () => {
    vi.spyOn(api, "getOrder").mockResolvedValue({ ...ORDER, lines: [{ ...ORDER.lines[0], title: "" }] });
    renderPage();
    expect(await screen.findByText("p1")).toBeInTheDocument();
  });
});
