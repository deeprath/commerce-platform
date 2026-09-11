import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import * as trackModule from "../track";
import type { Product, Shop as ShopT } from "../types";
import { Shop } from "./Shop";

vi.mock("../track", () => ({ track: vi.fn() }));

afterEach(() => {
  vi.restoreAllMocks();
});

const SHOP: ShopT = {
  id: "shop1", owner_id: "u1", name: "Trailhead Goods", slug: "trailhead-goods",
  description: "Camping gear", contact_email: "hi@trailhead.example", status: "SHOP_STATUS_ACTIVE",
  suspension_reason: "", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
};

const PRODUCT: Product = {
  id: "p1", slug: "trail-cap", title: "Trail Cap", description: "", category_id: "apparel",
  list_price: { currency_code: "USD", units: "24", nanos: 0 }, media_keys: [], status: "PRODUCT_STATUS_ACTIVE",
  attributes: {},
};

function renderPage(path = "/shops/trailhead-goods") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/shops/:slug" element={<Shop />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("Shop", () => {
  it("loads by slug and renders the shop and its products", async () => {
    const shop = vi.spyOn(api, "shop").mockResolvedValue(SHOP);
    const shopProducts = vi.spyOn(api, "shopProducts").mockResolvedValue({
      products: [PRODUCT], page: { next_page_token: "", total_size: "1" },
    });
    renderPage();
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Trailhead Goods" })).toBeInTheDocument();
    expect(shop).toHaveBeenCalledWith("trailhead-goods");
    expect(shopProducts).toHaveBeenCalledWith("trailhead-goods");
    expect(screen.getByText("Camping gear")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Trail Cap/ })).toHaveAttribute("href", "/p/trail-cap");
    expect(trackModule.track).toHaveBeenCalledWith("page_view", { path: "/shops/trailhead-goods" });
  });

  it("shows a not-found state", async () => {
    vi.spyOn(api, "shop").mockRejectedValue({ info: { reason: "SHOP_NOT_FOUND" } });
    vi.spyOn(api, "shopProducts").mockRejectedValue({ info: { reason: "SHOP_NOT_FOUND" } });
    renderPage();
    expect(await screen.findByText("That shop doesn’t exist.")).toBeInTheDocument();
  });

  it("shows a load error", async () => {
    vi.spyOn(api, "shop").mockRejectedValue(new Error("network down"));
    vi.spyOn(api, "shopProducts").mockResolvedValue({ products: [], page: { next_page_token: "", total_size: "0" } });
    renderPage();
    expect(await screen.findByText(/network down/)).toBeInTheDocument();
  });

  it("omits the description paragraph when the shop has none", async () => {
    vi.spyOn(api, "shop").mockResolvedValue({ ...SHOP, description: "" });
    vi.spyOn(api, "shopProducts").mockResolvedValue({ products: [], page: { next_page_token: "", total_size: "0" } });
    renderPage();
    await screen.findByRole("heading", { name: "Trailhead Goods" });
    expect(screen.queryByText("Camping gear")).not.toBeInTheDocument();
  });

  it("shows an empty-products message", async () => {
    vi.spyOn(api, "shop").mockResolvedValue(SHOP);
    vi.spyOn(api, "shopProducts").mockResolvedValue({ products: [], page: { next_page_token: "", total_size: "0" } });
    renderPage();
    expect(await screen.findByText("This shop has no products listed yet.")).toBeInTheDocument();
  });
});
