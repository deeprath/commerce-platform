import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiRequestError } from "../api";
import type { Shop } from "../types";
import { SellerDashboard } from "./SellerDashboard";

afterEach(() => {
  vi.restoreAllMocks();
});

const SHOP: Shop = {
  id: "shop_1",
  owner_id: "user_1",
  name: "Trailhead Goods",
  slug: "trailhead-goods",
  description: "Camping gear",
  contact_email: "hi@trailhead.example",
  status: "SHOP_STATUS_ACTIVE",
  suspension_reason: "",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function renderPage(authed: boolean) {
  return render(
    <MemoryRouter>
      <SellerDashboard authed={authed} />
    </MemoryRouter>,
  );
}

describe("SellerDashboard", () => {
  it("prompts sign-in when not authed, without calling the API", () => {
    const getMyShop = vi.spyOn(api, "getMyShop");
    renderPage(false);
    expect(screen.getByText(/sign in/i)).toBeInTheDocument();
    expect(getMyShop).not.toHaveBeenCalled();
  });

  it("shows the onboarding form on SHOP_NOT_FOUND, not stuck loading or errored", async () => {
    // Regression test: SellerDashboard used to track a separate "notfound"
    // boolean alongside `shop` that could fall out of sync with it. The
    // current design collapses "not fetched yet" and "confirmed no shop"
    // into the same `shop === null` state, driven only by this rejection.
    vi.spyOn(api, "getMyShop").mockRejectedValue(
      new ApiRequestError({ status: 404, code: "NOT_FOUND", reason: "SHOP_NOT_FOUND" }),
    );
    renderPage(true);
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
    expect(await screen.findByText("Become a seller")).toBeInTheDocument();
    expect(screen.queryByText(/loading/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/error/i)).not.toBeInTheDocument();
  });

  it("shows an error message for a non-SHOP_NOT_FOUND failure", async () => {
    vi.spyOn(api, "getMyShop").mockRejectedValue(
      new ApiRequestError({ status: 500, code: "INTERNAL", reason: "boom" }),
    );
    renderPage(true);
    expect(await screen.findByText(/INTERNAL/)).toBeInTheDocument();
    expect(screen.queryByText("Become a seller")).not.toBeInTheDocument();
  });

  it("renders the shop card once a shop is found", async () => {
    vi.spyOn(api, "getMyShop").mockResolvedValue(SHOP);
    renderPage(true);
    expect(await screen.findByRole("heading", { name: "Trailhead Goods" })).toBeInTheDocument();
    expect(screen.getByText("Active")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "/shops/trailhead-goods" })).toHaveAttribute(
      "href",
      "/shops/trailhead-goods",
    );
  });

  it("shows the suspension reason for a suspended shop, no live link", async () => {
    vi.spyOn(api, "getMyShop").mockResolvedValue({
      ...SHOP,
      status: "SHOP_STATUS_SUSPENDED",
      suspension_reason: "policy violation",
    });
    renderPage(true);
    expect(await screen.findByText(/Suspended: policy violation/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /shops\// })).not.toBeInTheDocument();
  });

  it("creates a shop from the onboarding form and switches to the shop card", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "getMyShop").mockRejectedValue(
      new ApiRequestError({ status: 404, code: "NOT_FOUND", reason: "SHOP_NOT_FOUND" }),
    );
    const createShop = vi.spyOn(api, "createShop").mockResolvedValue(SHOP);
    renderPage(true);

    await screen.findByText("Become a seller");
    await user.type(screen.getByLabelText(/shop name/i), "Trailhead Goods");
    await user.type(screen.getByLabelText(/contact email/i), "hi@trailhead.example");
    await user.click(screen.getByRole("button", { name: "Create shop" }));

    expect(createShop).toHaveBeenCalledWith(
      expect.objectContaining({ name: "Trailhead Goods", contact_email: "hi@trailhead.example" }),
    );
    expect(await screen.findByRole("heading", { name: "Trailhead Goods" })).toBeInTheDocument();
  });

  it("edits shop details and shows the update", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "getMyShop").mockResolvedValue(SHOP);
    const updated = { ...SHOP, name: "Trailhead Goods Co." };
    const updateShop = vi.spyOn(api, "updateShop").mockResolvedValue(updated);
    renderPage(true);

    await screen.findByRole("heading", { name: "Trailhead Goods" });
    await user.click(screen.getByRole("button", { name: "Edit shop details" }));

    const nameInput = screen.getByLabelText(/shop name/i);
    await user.clear(nameInput);
    await user.type(nameInput, "Trailhead Goods Co.");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(updateShop).toHaveBeenCalled());
    expect(await screen.findByRole("heading", { name: "Trailhead Goods Co." })).toBeInTheDocument();
    // Back to view mode — the edit form's Cancel button is gone.
    expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
  });

  it("cancels an edit without calling the API", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "getMyShop").mockResolvedValue(SHOP);
    const updateShop = vi.spyOn(api, "updateShop");
    renderPage(true);

    await screen.findByRole("heading", { name: "Trailhead Goods" });
    await user.click(screen.getByRole("button", { name: "Edit shop details" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(screen.getByRole("button", { name: "Edit shop details" })).toBeInTheDocument();
    expect(updateShop).not.toHaveBeenCalled();
  });
});
