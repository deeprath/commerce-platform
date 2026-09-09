import type { Product, SearchResponse, ApiError } from "./types";

// Same-origin: dev proxies /api to the BFF, prod serves the SPA and proxies
// /api from the same nginx. Cookies (httpOnly auth) ride along automatically.
const BASE = "/api/v1";

export class ApiRequestError extends Error {
  constructor(public readonly info: ApiError) {
    super(`${info.code}: ${info.reason}`);
    this.name = "ApiRequestError";
  }
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
    throw new ApiRequestError(info);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
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

  product(slug: string): Promise<{ product: Product }> {
    return req(`/catalog/products/${encodeURIComponent(slug)}`);
  },

  autocomplete(q: string): Promise<{ suggestions: string[] }> {
    return req(`/search/autocomplete?q=${encodeURIComponent(q)}`);
  },

  login(username: string, password: string): Promise<{ authenticated: boolean; expires_in: number }> {
    return req(`/auth/login`, { method: "POST", body: JSON.stringify({ username, password }) });
  },

  logout(): Promise<void> {
    return req(`/auth/logout`, { method: "POST" });
  },
};

// Resolve a media object key to a served URL. The BFF/CDN would normally do
// this; for local dev MinIO is on :9000.
export function mediaUrl(key: string): string {
  if (!key) return "";
  const base = import.meta.env.VITE_MEDIA_BASE_URL ?? "http://localhost:9000";
  return `${base}/${key}`;
}
