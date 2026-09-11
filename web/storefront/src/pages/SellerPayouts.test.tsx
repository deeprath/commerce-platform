import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { Payout } from "../types";
import { SellerPayouts } from "./SellerPayouts";

afterEach(() => {
  vi.restoreAllMocks();
});

const PAYOUT: Payout = {
  id: "payout_1",
  order_id: "order_1234567890",
  shop_id: "shop_1",
  amount: { currency_code: "USD", units: "42", nanos: 0 },
  status: "PAYOUT_STATUS_PAID",
  created_at: "2026-01-01T00:00:00Z",
  paid_at: "2026-01-02T00:00:00Z",
};

function renderPage(authed: boolean) {
  return render(
    <MemoryRouter>
      <SellerPayouts authed={authed} />
    </MemoryRouter>,
  );
}

describe("SellerPayouts", () => {
  it("prompts sign-in when not authed, without calling the API", () => {
    const list = vi.spyOn(api, "listShopPayouts");
    renderPage(false);
    expect(screen.getByText(/sign in/i)).toBeInTheDocument();
    expect(list).not.toHaveBeenCalled();
  });

  it("shows an empty state with no payouts", async () => {
    vi.spyOn(api, "listShopPayouts").mockResolvedValue({ payouts: [], page: { next_page_token: "", total_size: "0" } });
    renderPage(true);
    expect(await screen.findByText("No payouts yet.")).toBeInTheDocument();
  });

  it("shows a load error", async () => {
    vi.spyOn(api, "listShopPayouts").mockRejectedValue(new Error("network down"));
    renderPage(true);
    expect(await screen.findByText(/network down/)).toBeInTheDocument();
  });

  it("lists payouts with a truncated order id, amount, status and dates", async () => {
    vi.spyOn(api, "listShopPayouts").mockResolvedValue({
      payouts: [PAYOUT],
      page: { next_page_token: "", total_size: "1" },
    });
    renderPage(true);
    const row = (await screen.findByText("order_12")).closest("tr")!;
    expect(within(row).getByText("$42.00")).toBeInTheDocument();
    expect(within(row).getByText("Paid")).toBeInTheDocument();
  });

  it("shows an em dash for a pending payout with no paid_at", async () => {
    vi.spyOn(api, "listShopPayouts").mockResolvedValue({
      payouts: [{ ...PAYOUT, status: "PAYOUT_STATUS_PENDING", paid_at: "" }],
      page: { next_page_token: "", total_size: "1" },
    });
    renderPage(true);
    // "Pending" also names an <option> in the status filter, present before
    // the payouts load — scope the query to the results table (which only
    // renders once there's a payout) to land on the row, not the filter.
    const table = await screen.findByRole("table");
    const row = within(table).getByText("Pending").closest("tr")!;
    expect(within(row).getByText("—")).toBeInTheDocument();
  });

  it("refetches when the status filter changes", async () => {
    const user = userEvent.setup();
    const list = vi.spyOn(api, "listShopPayouts").mockResolvedValue({
      payouts: [],
      page: { next_page_token: "", total_size: "0" },
    });
    renderPage(true);

    await screen.findByText("No payouts yet.");
    expect(list).toHaveBeenCalledWith(undefined);

    await user.selectOptions(screen.getByRole("combobox"), "PAID");
    expect(list).toHaveBeenCalledWith("PAID");
  });
});
