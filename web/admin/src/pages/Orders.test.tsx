import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { Order } from "../types";
import { Orders } from "./Orders";

afterEach(() => {
  vi.restoreAllMocks();
});

const ORDER: Order = {
  id: "order_1234567890",
  owner_id: "owner_abcdefgh",
  status: "ORDER_STATUS_CONFIRMED",
  lines: [],
  subtotal: { currency_code: "USD", units: "20", nanos: 0 },
  discount: { currency_code: "USD", units: "0", nanos: 0 },
  tax: { currency_code: "USD", units: "2", nanos: 0 },
  total: { currency_code: "USD", units: "22", nanos: 0 },
  payment_id: "pay_1",
  cancel_reason: "",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function renderPage() {
  return render(
    <MemoryRouter>
      <Orders />
    </MemoryRouter>,
  );
}

describe("Orders (admin)", () => {
  it("shows a loading state, then an empty state", async () => {
    vi.spyOn(api, "listOrders").mockResolvedValue({ orders: [] });
    renderPage();
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
    expect(await screen.findByText("No orders.")).toBeInTheDocument();
  });

  it("shows a load error", async () => {
    vi.spyOn(api, "listOrders").mockRejectedValue(new Error("network down"));
    renderPage();
    expect(await screen.findByText("network down")).toBeInTheDocument();
  });

  it("lists orders with a truncated id/customer, status, total, and date", async () => {
    vi.spyOn(api, "listOrders").mockResolvedValue({ orders: [ORDER] });
    renderPage();
    const link = await screen.findByRole("link", { name: /order_12/ });
    expect(link).toHaveAttribute("href", "/orders/order_1234567890");
    const row = link.closest("tr")!;
    expect(within(row).getByText("Confirmed")).toBeInTheDocument();
    expect(within(row).getByText("$22.00")).toBeInTheDocument();
    expect(within(row).getByText("owner_ab")).toBeInTheDocument();
  });

  it("refetches when the status filter changes", async () => {
    const user = userEvent.setup();
    const listOrders = vi.spyOn(api, "listOrders").mockResolvedValue({ orders: [] });
    renderPage();
    await screen.findByText("No orders.");
    expect(listOrders).toHaveBeenCalledWith({ status: "" });

    await user.selectOptions(screen.getByRole("combobox"), "CANCELLED");
    await waitFor(() => expect(listOrders).toHaveBeenLastCalledWith({ status: "CANCELLED" }));
  });

  it("refetches when Refresh is clicked", async () => {
    const user = userEvent.setup();
    const listOrders = vi.spyOn(api, "listOrders").mockResolvedValue({ orders: [] });
    renderPage();
    await screen.findByText("No orders.");
    await user.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(listOrders).toHaveBeenCalledTimes(2));
  });
});
