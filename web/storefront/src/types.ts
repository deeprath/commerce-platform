// Shapes returned by the BFF (protojson of the gRPC contracts).

export interface Money {
  currency_code: string;
  units: string; // int64 as string in protojson
  nanos: number;
}

export interface Product {
  id: string;
  slug: string;
  title: string;
  description: string;
  category_id: string;
  list_price: Money;
  media_keys: string[];
  status: string;
  attributes: Record<string, string>;
}

export interface Hit {
  product_id: string;
  slug: string;
  title: string;
  category_id: string;
  list_price: Money;
  primary_media_key: string;
  score: number;
}

export interface FacetValue {
  value: string;
  count: string;
}

export interface Facet {
  field: string;
  values: FacetValue[];
}

export interface PageResponse {
  next_page_token: string;
  total_size: string;
}

export interface SearchResponse {
  hits: Hit[];
  facets: Facet[];
  page: PageResponse;
}

export interface ApiError {
  status: number;
  code: string;
  reason: string;
}

export function formatMoney(m: Money | undefined): string {
  if (!m) return "";
  const units = Number(m.units ?? 0);
  const cents = Math.round((m.nanos ?? 0) / 1e7);
  const amount = units + cents / 100;
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: m.currency_code || "USD",
    }).format(amount);
  } catch {
    return `${m.currency_code} ${amount.toFixed(2)}`;
  }
}
