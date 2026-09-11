import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiRequestError, mediaUrl, setUnauthorizedHandler } from "./api";
import { formatMoney } from "./types";

afterEach(() => {
  vi.restoreAllMocks();
  setUnauthorizedHandler(null);
});

function mockFetch(status: number, body: unknown) {
  // Fresh Response per call — a Response body can only be consumed once.
  return vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
    new Response(status === 204 ? null : JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

describe("api", () => {
  it("builds the browse query string", async () => {
    const f = mockFetch(200, { hits: [], facets: [], page: {} });
    await api.browse({ q: "tee", category_id: "apparel", sort: "price_asc", page_size: 24 });
    const url = f.mock.calls[0][0] as string;
    expect(url).toContain("/api/v1/catalog/products?");
    expect(url).toContain("q=tee");
    expect(url).toContain("category_id=apparel");
    expect(url).toContain("sort=price_asc");
    expect(url).toContain("page_size=24");
  });

  it("sends credentials so the auth cookie rides along", async () => {
    const f = mockFetch(200, { product: {} });
    await api.product("trail-cap");
    expect(f.mock.calls[0][1]).toMatchObject({ credentials: "include" });
  });

  it("throws ApiRequestError with the parsed body on failure", async () => {
    mockFetch(404, { code: "NOT_FOUND", reason: "PRODUCT_NOT_FOUND" });
    await expect(api.product("nope")).rejects.toBeInstanceOf(ApiRequestError);
    try {
      await api.product("nope");
    } catch (e) {
      expect((e as ApiRequestError).info).toMatchObject({
        status: 404,
        reason: "PRODUCT_NOT_FOUND",
      });
    }
  });

  it("returns undefined for 204", async () => {
    mockFetch(204, null);
    await expect(api.logout()).resolves.toBeUndefined();
  });

  it("normalises an empty cart so items is always an array", async () => {
    // The BFF omits/nulls `items` for an empty cart; the UI must never see null.
    mockFetch(200, { id: "c_1", total_quantity: 0 });
    const cart = await api.getCart();
    expect(cart.items).toEqual([]);
  });

  it("fires the unauthorized handler on a 401, then still throws", async () => {
    mockFetch(401, { code: "UNAUTHENTICATED", reason: "SIGN_IN_REQUIRED" });
    const onUnauth = vi.fn();
    setUnauthorizedHandler(onUnauth);
    await expect(api.listOrders()).rejects.toBeInstanceOf(ApiRequestError);
    expect(onUnauth).toHaveBeenCalledOnce();
  });

  it("does not fire the unauthorized handler on other errors", async () => {
    mockFetch(404, { code: "NOT_FOUND", reason: "ORDER_NOT_FOUND" });
    const onUnauth = vi.fn();
    setUnauthorizedHandler(onUnauth);
    await expect(api.getOrder("nope")).rejects.toBeInstanceOf(ApiRequestError);
    expect(onUnauth).not.toHaveBeenCalled();
  });

  it("fetches a shop by slug", async () => {
    const f = mockFetch(200, { id: "shop-1", slug: "the-shop", name: "The Shop" });
    const shop = await api.shop("the-shop");
    expect(f.mock.calls[0][0]).toBe("/api/v1/shops/the-shop");
    expect(shop.name).toBe("The Shop");
  });

  it("lists a shop's products, forwarding a page token when given", async () => {
    const f = mockFetch(200, { products: [], page: {} });
    await api.shopProducts("the-shop", "cursor-1");
    const url = f.mock.calls[0][0] as string;
    expect(url).toContain("/api/v1/shops/the-shop/products?");
    expect(url).toContain("page_token=cursor-1");
  });

  it("omits the query string when no page token is given", async () => {
    const f = mockFetch(200, { products: [], page: {} });
    await api.shopProducts("the-shop");
    expect(f.mock.calls[0][0]).toBe("/api/v1/shops/the-shop/products");
  });
});

describe("seller api", () => {
  it("creates a shop", async () => {
    const f = mockFetch(201, { id: "shop-1", name: "My Shop" });
    await api.createShop({ name: "My Shop", description: "d", contact_email: "a@b.com" });
    expect(f.mock.calls[0][0]).toBe("/api/v1/seller/shops");
    expect(f.mock.calls[0][1]).toMatchObject({ method: "POST" });
    expect(JSON.parse(f.mock.calls[0][1]!.body as string)).toMatchObject({ name: "My Shop" });
  });

  it("gets and updates the caller's own shop", async () => {
    const f = mockFetch(200, { id: "shop-1", name: "My Shop" });
    await api.getMyShop();
    expect(f.mock.calls[0][0]).toBe("/api/v1/seller/shops/me");

    await api.updateShop({ name: "New Name" });
    expect(f.mock.calls[1][0]).toBe("/api/v1/seller/shops/me");
    expect(f.mock.calls[1][1]).toMatchObject({ method: "PUT" });
  });

  it("lists, adds, and removes shop staff", async () => {
    const f = mockFetch(200, { owner_subject: "owner-1", staff_subjects: ["helper-a"] });
    await api.listShopStaff();
    expect(f.mock.calls[0][0]).toBe("/api/v1/seller/shops/me/staff");

    await api.addShopStaff("helper-b");
    expect(f.mock.calls[1][1]).toMatchObject({ method: "POST" });
    expect(JSON.parse(f.mock.calls[1][1]!.body as string)).toEqual({ subject: "helper-b" });

    await api.removeShopStaff("helper-b");
    expect(f.mock.calls[2][0]).toBe("/api/v1/seller/shops/me/staff/helper-b");
    expect(f.mock.calls[2][1]).toMatchObject({ method: "DELETE" });
  });

  it("lists the caller's own shop products, with an optional page token", async () => {
    const f = mockFetch(200, { products: [], page: {} });
    await api.listMyShopProducts();
    expect(f.mock.calls[0][0]).toBe("/api/v1/seller/products");
    await api.listMyShopProducts("cursor-2");
    expect(f.mock.calls[1][0]).toContain("page_token=cursor-2");
  });

  it("creates, updates, and archives a shop product", async () => {
    const f = mockFetch(201, { product: { id: "p1" } });
    await api.createShopProduct({
      slug: "widget",
      title: "Widget",
      category_id: "c1",
      list_price: { currency_code: "USD", units: "10" },
    });
    expect(f.mock.calls[0][0]).toBe("/api/v1/seller/products");
    expect(f.mock.calls[0][1]).toMatchObject({ method: "POST" });

    await api.updateShopProduct("p1", {
      title: "Widget 2",
      category_id: "c1",
      list_price: { currency_code: "USD", units: "12" },
      status: "PRODUCT_STATUS_ACTIVE",
    });
    expect(f.mock.calls[1][0]).toBe("/api/v1/seller/products/p1");
    expect(f.mock.calls[1][1]).toMatchObject({ method: "PUT" });

    await api.archiveShopProduct("p1");
    expect(f.mock.calls[2][0]).toBe("/api/v1/seller/products/p1/archive");
    expect(f.mock.calls[2][1]).toMatchObject({ method: "POST" });
  });

  it("lists shop payouts, forwarding status and page token filters", async () => {
    const f = mockFetch(200, { payouts: [], page: {} });
    await api.listShopPayouts();
    expect(f.mock.calls[0][0]).toBe("/api/v1/seller/payouts");

    await api.listShopPayouts("PENDING", "cursor-3");
    const url = f.mock.calls[1][0] as string;
    expect(url).toContain("status=PENDING");
    expect(url).toContain("page_token=cursor-3");
  });
});

describe("formatMoney", () => {
  it("combines units and nanos", () => {
    expect(formatMoney({ currency_code: "USD", units: "29", nanos: 990000000 })).toBe("$29.99");
  });
  it("handles a missing value", () => {
    expect(formatMoney(undefined)).toBe("");
  });
});

describe("mediaUrl", () => {
  it("joins base and key", () => {
    expect(mediaUrl("product-media/a/b.jpg")).toContain("/product-media/a/b.jpg");
  });
  it("is empty for an empty key", () => {
    expect(mediaUrl("")).toBe("");
  });
});
