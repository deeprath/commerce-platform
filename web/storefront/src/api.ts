import type {
  Product,
  SearchResponse,
  ApiError,
  CartView,
  Order,
  CheckoutResponse,
  SoldBy,
  Shop,
  ProductListResponse,
} from "./types";

export interface Address {
  full_name: string;
  line1: string;
  line2?: string;
  city: string;
  region: string;
  postal_code: string;
  country_code: string;
  phone?: string;
}

// Same-origin: dev proxies /api to the BFF, prod serves the SPA and proxies
// /api from the same nginx. Cookies (httpOnly auth) ride along automatically.
const BASE = "/api/v1";

export class ApiRequestError extends Error {
  constructor(public readonly info: ApiError) {
    super(`${info.code}: ${info.reason}`);
    this.name = "ApiRequestError";
  }
}

// Called whenever a request comes back 401. The app registers a handler that
// drops its cached "signed in" flag, so protected views fall back to a sign-in
// prompt instead of surfacing a raw UNAUTHENTICATED error when the httpOnly
// session cookie has expired out from under the client.
let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn;
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(BASE + path, {
    credentials: "include",
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
    ...init,
  });
  if (!res.ok) {
    let info: ApiError = { status: res.status, code: "ERROR", reason: res.statusText };
    try {
      info = { status: res.status, ...(await res.json()) };
    } catch {
      /* body was not JSON */
    }
    if (res.status === 401) onUnauthorized?.();
    throw new ApiRequestError(info);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// An empty cart may come back with items omitted/null depending on the caller;
// normalise so the UI can always treat it as an array.
function normCart(c: CartView): CartView {
  return { ...c, items: c.items ?? [] };
}

export interface BrowseParams {
  q?: string;
  category_id?: string;
  sort?: "newest" | "price_asc" | "price_desc";
  page_size?: number;
  page_token?: string;
}

export const api = {
  browse(p: BrowseParams): Promise<SearchResponse> {
    const qs = new URLSearchParams();
    if (p.q) qs.set("q", p.q);
    if (p.category_id) qs.set("category_id", p.category_id);
    if (p.sort) qs.set("sort", p.sort);
    if (p.page_size) qs.set("page_size", String(p.page_size));
    if (p.page_token) qs.set("page_token", p.page_token);
    return req<SearchResponse>(`/catalog/products?${qs.toString()}`);
  },

  product(slug: string): Promise<{ product: Product; sold_by?: SoldBy }> {
    return req(`/catalog/products/${encodeURIComponent(slug)}`);
  },

  autocomplete(q: string): Promise<{ suggestions: string[] }> {
    return req(`/search/autocomplete?q=${encodeURIComponent(q)}`);
  },

  // --- marketplace storefront pages ---
  shop(slug: string): Promise<Shop> {
    return req(`/shops/${encodeURIComponent(slug)}`);
  },
  shopProducts(slug: string, pageToken?: string): Promise<ProductListResponse> {
    const qs = new URLSearchParams();
    if (pageToken) qs.set("page_token", pageToken);
    const q = qs.toString();
    return req(`/shops/${encodeURIComponent(slug)}/products${q ? `?${q}` : ""}`);
  },

  login(username: string, password: string): Promise<{ authenticated: boolean; expires_in: number }> {
    return req(`/auth/login`, { method: "POST", body: JSON.stringify({ username, password }) });
  },

  logout(): Promise<void> {
    return req(`/auth/logout`, { method: "POST" });
  },

  // --- cart ---
  getCart(coupon?: string): Promise<CartView> {
    return req<CartView>(`/cart${coupon ? `?coupon=${encodeURIComponent(coupon)}` : ""}`).then(
      normCart,
    );
  },
  addToCart(productId: string, quantity = 1): Promise<CartView> {
    return req<CartView>(`/cart/items`, {
      method: "POST",
      body: JSON.stringify({ product_id: productId, quantity }),
    }).then(normCart);
  },
  setCartQuantity(productId: string, quantity: number): Promise<CartView> {
    return req<CartView>(`/cart/items/${encodeURIComponent(productId)}`, {
      method: "PUT",
      body: JSON.stringify({ quantity }),
    }).then(normCart);
  },
  removeFromCart(productId: string): Promise<CartView> {
    return req<CartView>(`/cart/items/${encodeURIComponent(productId)}`, { method: "DELETE" }).then(
      normCart,
    );
  },
  clearCart(): Promise<CartView> {
    return req<CartView>(`/cart/clear`, { method: "POST" }).then(normCart);
  },

  // --- checkout & orders ---
  checkout(body: {
    ship_to: Address;
    coupon_code?: string;
    currency_code?: string;
    payment_method_token: string;
  }): Promise<CheckoutResponse> {
    return req(`/checkout`, { method: "POST", body: JSON.stringify(body) });
  },
  confirmCheckout(paymentId: string, outcome: "authorize" | "fail"): Promise<{ status: string }> {
    return req(`/checkout/confirm`, {
      method: "POST",
      body: JSON.stringify({ payment_id: paymentId, outcome }),
    });
  },
  listOrders(): Promise<{ orders: Order[] }> {
    return req(`/orders`);
  },
  getOrder(id: string): Promise<Order> {
    return req(`/orders/${encodeURIComponent(id)}`);
  },
};

// Resolve a media object key to a served URL. The BFF/CDN would normally do
// this; for local dev MinIO is on :9000.
export function mediaUrl(key: string): string {
  if (!key) return "";
  const base = import.meta.env.VITE_MEDIA_BASE_URL ?? "http://localhost:9000";
  return `${base}/${key}`;
}
