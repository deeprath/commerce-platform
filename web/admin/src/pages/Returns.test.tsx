import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiRequestError } from "../api";
import type { Return } from "../types";
import { Returns } from "./Returns";

afterEach(() => {
  vi.restoreAllMocks();
});

const RETURN: Return = {
  id: "return_1234567890",
  order_id: "order_1234567890",
  owner_id: "owner_1",
  status: "RETURN_STATUS_REQUESTED",
  reason: "defective",
  lines: [{ product_id: "p1", quantity: 2, refund_amount: { currency_code: "USD", units: "20", nanos: 0 } }],
  refund_total: { currency_code: "USD", units: "20", nanos: 0 },
  decided_by: "",
  decision_note: "",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

describe("Returns", () => {
  it("shows a loading state, then an empty state", async () => {
    vi.spyOn(api, "listReturns").mockResolvedValue({ returns: [] });
    render(<Returns />);
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
    expect(await screen.findByText("Nothing here.")).toBeInTheDocument();
  });

  it("defaults to the REQUESTED filter", async () => {
    const listReturns = vi.spyOn(api, "listReturns").mockResolvedValue({ returns: [] });
    render(<Returns />);
    await screen.findByText("Nothing here.");
    expect(listReturns).toHaveBeenCalledWith({ status: "REQUESTED" });
  });

  it("shows a load error", async () => {
    vi.spyOn(api, "listReturns").mockRejectedValue(new Error("network down"));
    render(<Returns />);
    expect(await screen.findByText("network down")).toBeInTheDocument();
  });

  it("lists a return's order, items, refund, reason, and date", async () => {
    vi.spyOn(api, "listReturns").mockResolvedValue({ returns: [RETURN] });
    render(<Returns />);
    const row = (await screen.findByText("defective")).closest("tr")!;
    expect(row).toHaveTextContent("#return_1");
    expect(within(row).getByText("order_12")).toBeInTheDocument();
    expect(within(row).getByText("2× p1")).toBeInTheDocument();
    expect(within(row).getByText("$20.00")).toBeInTheDocument();
    expect(within(row).getByText("Requested")).toBeInTheDocument();
  });

  it("shows an em dash when there's no reason", async () => {
    vi.spyOn(api, "listReturns").mockResolvedValue({ returns: [{ ...RETURN, reason: "" }] });
    render(<Returns />);
    expect(await screen.findByText("—")).toBeInTheDocument();
  });

  it("only shows Approve/Reject for a REQUESTED return", async () => {
    vi.spyOn(api, "listReturns").mockResolvedValue({
      returns: [{ ...RETURN, status: "RETURN_STATUS_APPROVED" }],
    });
    render(<Returns />);
    await screen.findByText("defective");
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reject" })).not.toBeInTheDocument();
  });

  it("approves a return and shows the resulting status", async () => {
    const user = userEvent.setup();
    const listReturns = vi.spyOn(api, "listReturns").mockResolvedValue({ returns: [RETURN] });
    const decideReturn = vi.spyOn(api, "decideReturn").mockResolvedValue({ ...RETURN, status: "RETURN_STATUS_APPROVED" });
    render(<Returns />);
    await screen.findByText("defective");

    await user.click(screen.getByRole("button", { name: "Approve" }));

    expect(decideReturn).toHaveBeenCalledWith("return_1234567890", true, "Approved from admin");
    expect(await screen.findByText("Return #return_1 → Approved")).toBeInTheDocument();
    await waitFor(() => expect(listReturns).toHaveBeenCalledTimes(2));
  });

  it("rejects a return", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "listReturns").mockResolvedValue({ returns: [RETURN] });
    const decideReturn = vi.spyOn(api, "decideReturn").mockResolvedValue({ ...RETURN, status: "RETURN_STATUS_REJECTED" });
    render(<Returns />);
    await screen.findByText("defective");

    await user.click(screen.getByRole("button", { name: "Reject" }));

    expect(decideReturn).toHaveBeenCalledWith("return_1234567890", false, "Rejected from admin");
    expect(await screen.findByText("Return #return_1 → Rejected")).toBeInTheDocument();
  });

  it("shows the API's reason when a decision fails", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "listReturns").mockResolvedValue({ returns: [RETURN] });
    vi.spyOn(api, "decideReturn").mockRejectedValue(
      new ApiRequestError({ status: 409, code: "CONFLICT", reason: "ALREADY_DECIDED" }),
    );
    render(<Returns />);
    await screen.findByText("defective");

    await user.click(screen.getByRole("button", { name: "Approve" }));

    expect(await screen.findByText("ALREADY_DECIDED")).toBeInTheDocument();
  });

  it("refetches when the status filter changes", async () => {
    const user = userEvent.setup();
    const listReturns = vi.spyOn(api, "listReturns").mockResolvedValue({ returns: [] });
    render(<Returns />);
    await screen.findByText("Nothing here.");

    await user.selectOptions(screen.getByRole("combobox"), "APPROVED");
    await waitFor(() => expect(listReturns).toHaveBeenLastCalledWith({ status: "APPROVED" }));
  });
});
