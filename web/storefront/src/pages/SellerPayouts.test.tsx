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

// Columns: Order | Amount | Returned | Net | Status | Created | Paid.
// Amount and Net carry the same text when nothing is reversed, and both
// Returned and Paid render an em dash when empty, so these assertions address
// cells by position rather than by text.
const COL = { order: 0, amount: 1, returned: 2, net: 3, status: 4, created: 5, paid: 6 };

function cells(row: HTMLElement): HTMLTableCellElement[] {
  return Array.from(row.querySelectorAll("td"));
}

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
    const td = cells(row);
    expect(td[COL.amount]).toHaveTextContent("$42.00");
    // Nothing reversed, so net settles at the full amount.
    expect(td[COL.returned]).toHaveTextContent("—");
    expect(td[COL.net]).toHaveTextContent("$42.00");
    expect(td[COL.status]).toHaveTextContent("Paid");
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
    expect(cells(row)[COL.paid]).toHaveTextContent("—");
  });

  it("shows a partial reversal as a negative, with net below the gross amount", async () => {
    vi.spyOn(api, "listShopPayouts").mockResolvedValue({
      payouts: [
        {
          ...PAYOUT,
          status: "PAYOUT_STATUS_PENDING",
          paid_at: "",
          reversed_amount: { currency_code: "USD", units: "10", nanos: 0 },
          reversed_at: "2026-01-03T00:00:00Z",
        },
      ],
      page: { next_page_token: "", total_size: "1" },
    });
    renderPage(true);

    const table = await screen.findByRole("table");
    const td = cells(within(table).getByText("Pending").closest("tr")!);
    expect(td[COL.returned]).toHaveTextContent("−$10.00");
    expect(td[COL.net]).toHaveTextContent("$32.00");
    // A partly reversed payout is still PENDING — the amount column alone
    // would overstate what the shop is owed, which is why Net exists.
    expect(td[COL.amount]).toHaveTextContent("$42.00");
  });

  it("labels a fully reversed payout and settles it at zero", async () => {
    vi.spyOn(api, "listShopPayouts").mockResolvedValue({
      payouts: [
        {
          ...PAYOUT,
          status: "PAYOUT_STATUS_REVERSED",
          reversed_amount: { currency_code: "USD", units: "42", nanos: 0 },
          reversed_at: "2026-01-03T00:00:00Z",
        },
      ],
      page: { next_page_token: "", total_size: "1" },
    });
    renderPage(true);

    const table = await screen.findByRole("table");
    const td = cells(within(table).getByText("Reversed").closest("tr")!);
    expect(td[COL.returned]).toHaveTextContent("−$42.00");
    expect(td[COL.net]).toHaveTextContent("$0.00");
  });

  it("handles a payout with no reversal field at all", async () => {
    // The BFF passes the proto through, and an older payout row predates the
    // field — an absent reversed_amount must read as zero, not NaN.
    vi.spyOn(api, "listShopPayouts").mockResolvedValue({
      payouts: [{ ...PAYOUT, reversed_amount: undefined }],
      page: { next_page_token: "", total_size: "1" },
    });
    renderPage(true);

    // Located by order id, not by "Paid" — that word is also the last column
    // header, so a text query inside the table matches two elements.
    const td = cells((await screen.findByText("order_12")).closest("tr")!);
    expect(td[COL.returned]).toHaveTextContent("—");
    expect(td[COL.net]).toHaveTextContent("$42.00");
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
