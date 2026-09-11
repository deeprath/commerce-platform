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
