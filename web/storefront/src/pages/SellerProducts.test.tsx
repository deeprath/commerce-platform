import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { Product } from "../types";
import { SellerProducts } from "./SellerProducts";

afterEach(() => {
  vi.restoreAllMocks();
});

const PRODUCT: Product = {
  id: "prod_1",
  slug: "trail-cap",
  title: "Trail Cap",
  description: "A cap",
  category_id: "apparel",
  list_price: { currency_code: "USD", units: "24", nanos: 500000000 },
  media_keys: [],
  status: "PRODUCT_STATUS_ACTIVE",
  attributes: {},
};

function renderPage(authed: boolean) {
  return render(
    <MemoryRouter>
      <SellerProducts authed={authed} />
    </MemoryRouter>,
  );
}

describe("SellerProducts", () => {
  it("prompts sign-in when not authed, without calling the API", () => {
    const list = vi.spyOn(api, "listMyShopProducts");
    renderPage(false);
    expect(screen.getByText(/sign in/i)).toBeInTheDocument();
    expect(list).not.toHaveBeenCalled();
  });

  it("shows an empty state with no products", async () => {
    vi.spyOn(api, "listMyShopProducts").mockResolvedValue({ products: [], page: { next_page_token: "", total_size: "0" } });
    renderPage(true);
    expect(await screen.findByText("No products yet.")).toBeInTheDocument();
  });

  it("shows a load error", async () => {
    vi.spyOn(api, "listMyShopProducts").mockRejectedValue(new Error("network down"));
    renderPage(true);
    expect(await screen.findByText(/network down/)).toBeInTheDocument();
  });

  it("lists products with their price and status", async () => {
    vi.spyOn(api, "listMyShopProducts").mockResolvedValue({
      products: [PRODUCT],
      page: { next_page_token: "", total_size: "1" },
    });
    renderPage(true);
    const row = (await screen.findByText("Trail Cap")).closest("tr")!;
    expect(within(row).getByText("Active")).toBeInTheDocument();
    expect(within(row).getByText("$24.50")).toBeInTheDocument();
  });

  it("creates a product from the new-product form and reloads the list", async () => {
    const user = userEvent.setup();
    const list = vi
      .spyOn(api, "listMyShopProducts")
      .mockResolvedValueOnce({ products: [], page: { next_page_token: "", total_size: "0" } })
      .mockResolvedValueOnce({ products: [PRODUCT], page: { next_page_token: "", total_size: "1" } });
    const create = vi.spyOn(api, "createShopProduct").mockResolvedValue({ product: PRODUCT });
    renderPage(true);

    await screen.findByText("No products yet.");
    await user.click(screen.getByRole("button", { name: "+ New product" }));
    await user.type(screen.getByLabelText(/slug/i), "trail-cap");
    await user.type(screen.getByLabelText(/^title/i), "Trail Cap");
    await user.type(screen.getByLabelText(/category/i), "apparel");
    const priceInput = screen.getByLabelText(/price/i);
    await user.clear(priceInput);
    // A number input normalizes "24.50" down to "24.5" (drops the trailing
    // zero), so the parsed nanos reflects one decimal digit, not two.
    await user.type(priceInput, "24.5");
    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        expect.objectContaining({
          slug: "trail-cap",
          title: "Trail Cap",
          category_id: "apparel",
          list_price: { currency_code: "USD", units: "24", nanos: 50000000 },
        }),
      ),
    );
    expect(list).toHaveBeenCalledTimes(2);
    // The create form closes back down after a successful submit.
    expect(screen.queryByRole("button", { name: "Create" })).not.toBeInTheDocument();
  });

  it("edits an existing product in place", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "listMyShopProducts").mockResolvedValue({
      products: [PRODUCT],
      page: { next_page_token: "", total_size: "1" },
    });
    const update = vi.spyOn(api, "updateShopProduct").mockResolvedValue({ product: PRODUCT });
    renderPage(true);

    await screen.findByText("Trail Cap");
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(update).toHaveBeenCalledWith("prod_1", expect.objectContaining({ title: "Trail Cap" })));
  });

  it("archives a product after confirmation and reloads", async () => {
    const user = userEvent.setup();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const list = vi.spyOn(api, "listMyShopProducts").mockResolvedValue({
      products: [PRODUCT],
      page: { next_page_token: "", total_size: "1" },
    });
    const archive = vi.spyOn(api, "archiveShopProduct").mockResolvedValue(undefined);
    renderPage(true);

    await screen.findByText("Trail Cap");
    await user.click(screen.getByRole("button", { name: "Archive" }));

    expect(archive).toHaveBeenCalledWith("prod_1");
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  });

  it("does not archive when the confirmation is declined", async () => {
    const user = userEvent.setup();
    vi.spyOn(window, "confirm").mockReturnValue(false);
    vi.spyOn(api, "listMyShopProducts").mockResolvedValue({
      products: [PRODUCT],
      page: { next_page_token: "", total_size: "1" },
    });
    const archive = vi.spyOn(api, "archiveShopProduct");
    renderPage(true);

    await screen.findByText("Trail Cap");
    await user.click(screen.getByRole("button", { name: "Archive" }));

    expect(archive).not.toHaveBeenCalled();
  });
});
