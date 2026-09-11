import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { CartCtx } from "../cart-context";
import { useCart } from "../cart-context";
import * as trackModule from "../track";
import type { Product as P } from "../types";
import { Product } from "./Product";

vi.mock("../cart-context", () => ({ useCart: vi.fn() }));
vi.mock("../track", () => ({ track: vi.fn() }));

const navigate = vi.fn();
vi.mock("react-router-dom", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-router-dom")>();
  return { ...actual, useNavigate: () => navigate };
});

const mockUseCart = vi.mocked(useCart);

afterEach(() => {
  vi.restoreAllMocks();
  navigate.mockClear();
});

const PRODUCT: P = {
  id: "p1",
  slug: "trail-cap",
  title: "Trail Cap",
  description: "A cap for the trail",
  category_id: "apparel",
  list_price: { currency_code: "USD", units: "24", nanos: 0 },
  media_keys: [],
  status: "PRODUCT_STATUS_ACTIVE",
  attributes: {},
};

function cartCtx(overrides: Partial<CartCtx> = {}): CartCtx {
  return {
    cart: null, count: 0, loading: false,
    refresh: vi.fn(), add: vi.fn().mockResolvedValue(undefined),
    setQty: vi.fn(), remove: vi.fn(), clear: vi.fn(),
    ...overrides,
  };
}

function renderPage(path = "/p/trail-cap") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/p/:slug" element={<Product />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("Product", () => {
  it("loads by slug and renders the product", async () => {
    mockUseCart.mockReturnValue(cartCtx());
    const product = vi.spyOn(api, "product").mockResolvedValue({ product: PRODUCT });
    renderPage();
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Trail Cap" })).toBeInTheDocument();
    expect(product).toHaveBeenCalledWith("trail-cap");
    expect(screen.getByText("A cap for the trail")).toBeInTheDocument();
    expect(trackModule.track).toHaveBeenCalledWith("product_view", { product_id: "p1", path: "/p/trail-cap" });
  });

  it("shows a not-found state", async () => {
    mockUseCart.mockReturnValue(cartCtx());
    vi.spyOn(api, "product").mockRejectedValue({ info: { reason: "PRODUCT_NOT_FOUND" } });
    renderPage();
    expect(await screen.findByText("That product doesn’t exist.")).toBeInTheDocument();
  });

  it("shows a load error", async () => {
    mockUseCart.mockReturnValue(cartCtx());
    vi.spyOn(api, "product").mockRejectedValue(new Error("network down"));
    renderPage();
    expect(await screen.findByText(/network down/)).toBeInTheDocument();
  });

  it("shows the sold-by link for a marketplace product", async () => {
    mockUseCart.mockReturnValue(cartCtx());
    vi.spyOn(api, "product").mockResolvedValue({
      product: PRODUCT, sold_by: { id: "shop1", name: "Trailhead Goods", slug: "trailhead-goods" },
    });
    renderPage();
    const link = await screen.findByRole("link", { name: "Trailhead Goods" });
    expect(link).toHaveAttribute("href", "/shops/trailhead-goods");
  });

  it("omits the sold-by line for a first-party product", async () => {
    mockUseCart.mockReturnValue(cartCtx());
    vi.spyOn(api, "product").mockResolvedValue({ product: PRODUCT });
    renderPage();
    await screen.findByRole("heading", { name: "Trail Cap" });
    expect(screen.queryByText(/Sold by/)).not.toBeInTheDocument();
  });

  it("renders attributes when present", async () => {
    mockUseCart.mockReturnValue(cartCtx());
    vi.spyOn(api, "product").mockResolvedValue({
      product: { ...PRODUCT, attributes: { color: "Red", size: "M" } },
    });
    renderPage();
    await screen.findByRole("heading", { name: "Trail Cap" });
    expect(screen.getByText("color")).toBeInTheDocument();
    expect(screen.getByText("Red")).toBeInTheDocument();
  });

  it("adds to cart and navigates there", async () => {
    const user = userEvent.setup();
    const add = vi.fn().mockResolvedValue(undefined);
    mockUseCart.mockReturnValue(cartCtx({ add }));
    vi.spyOn(api, "product").mockResolvedValue({ product: PRODUCT });
    renderPage();
    await screen.findByRole("heading", { name: "Trail Cap" });

    await user.click(screen.getByRole("button", { name: "Add to cart" }));

    expect(add).toHaveBeenCalledWith("p1", 1);
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/cart"));
  });
});
