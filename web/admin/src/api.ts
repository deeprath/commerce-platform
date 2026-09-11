import type { ApiError, Order, Product, Return, Shipment } from "./types";

const BASE = "/api/v1";

export class ApiRequestError extends Error {
  constructor(public readonly info: ApiError) {
    super(`${info.code}: ${info.reason}`);
    this.name = "ApiRequestError";
  }
}

// Fires on any 401 so the app can drop its "signed in" flag and show the login
// screen instead of a raw error when the session cookie has expired.
let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn;
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(BASE + path, {
    credentials: "include",
    headers: { "Content-Type": "application/json", ...init?.headers },
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

function qs(params: Record<string, string | number | undefined>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "") p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

export const api = {
  login(username: string, password: string) {
    return req<{ authenticated: boolean }>(`/auth/login`, {
      method: "POST",
      body: JSON.stringify({ username, password }),
    });
  },
  logout() {
    return req<void>(`/auth/logout`, { method: "POST" });
  },

  // --- products ---
  listProducts(params: { q?: string; page_size?: number } = {}) {
    return req<{ hits: Array<{ product_id: string; slug: string; title: string; list_price: import("./types").Money }> }>(
      `/catalog/products${qs({ ...params, page_size: params.page_size ?? 50 })}`,
    );
  },
  createProduct(body: {
    slug: string;
    title: string;
    description: string;
    category_id: string;
    list_price: { currency_code: string; units: string; nanos: number };
  }) {
    return req<Product>(`/admin/catalog/products`, { method: "POST", body: JSON.stringify(body) });
  },
  archiveProduct(id: string) {
    return req<Product>(`/admin/catalog/products/${encodeURIComponent(id)}/archive`, { method: "POST" });
  },

  // --- orders ---
  listOrders(params: { status?: string; owner_id?: string; page_size?: number } = {}) {
    return req<{ orders: Order[]; page?: { next_page_token: string } }>(`/admin/orders${qs(params)}`);
  },
  getOrder(id: string) {
    return req<Order>(`/admin/orders/${encodeURIComponent(id)}`);
  },

  // --- returns ---
  listReturns(params: { status?: string; page_size?: number } = {}) {
    return req<{ returns: Return[] }>(`/admin/returns${qs(params)}`);
  },
  decideReturn(id: string, approve: boolean, note: string) {
    return req<Return>(`/admin/returns/${encodeURIComponent(id)}/decide`, {
      method: "POST",
      body: JSON.stringify({ approve, note }),
    });
  },

  // --- shipments ---
  listShipments(params: { status?: string; order_id?: string; page_size?: number } = {}) {
    return req<{ shipments: Shipment[] }>(`/admin/shipments${qs(params)}`);
  },
  shipShipment(id: string, carrier: string, tracking_number: string) {
    return req<Shipment>(`/admin/shipments/${encodeURIComponent(id)}/ship`, {
      method: "POST",
      body: JSON.stringify({ carrier, tracking_number }),
    });
  },
  deliverShipment(id: string) {
    return req<Shipment>(`/admin/shipments/${encodeURIComponent(id)}/deliver`, { method: "POST" });
  },
  cancelShipment(id: string, reason: string) {
    return req<Shipment>(`/admin/shipments/${encodeURIComponent(id)}/cancel`, {
      method: "POST",
      body: JSON.stringify({ reason }),
    });
  },
};

export { qs as _qs }; // exported for tests
