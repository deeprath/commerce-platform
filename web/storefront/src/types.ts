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
  // Empty => first-party (platform-owned) product.
  shop_id?: string;
}

// Attached by the BFF alongside a marketplace product's GetProduct response;
// absent for first-party products.
export interface SoldBy {
  id: string;
  name: string;
  slug: string;
}

export interface Shop {
  id: string;
  owner_id: string;
  name: string;
  slug: string;
  description: string;
  contact_email: string;
  status: string;
  suspension_reason: string;
  created_at: string;
  updated_at: string;
}

export interface ProductListResponse {
  products: Product[];
  page: PageResponse;
}

export interface ShopStaff {
  owner_subject: string;
  staff_subjects: string[];
}

export interface Payout {
  id: string;
  order_id: string;
  shop_id: string;
  amount: Money;
  status: string;
  created_at: string;
  paid_at: string;
}

export interface PayoutListResponse {
  payouts: Payout[];
  page: PageResponse;
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

export interface CartLine {
  product_id: string;
  slug: string;
  title: string;
  quantity: number;
  unit_price?: Money;
  line_total?: Money;
  primary_media_key: string;
}

export interface CartView {
  id: string;
  items: CartLine[];
  total_quantity: number;
  subtotal?: Money;
  discount?: Money;
  tax?: Money;
  total?: Money;
  coupon_code?: string;
  coupon_error?: string;
}

export interface OrderLine {
  product_id: string;
  title: string;
  quantity: number;
  unit_price: Money;
  line_total: Money;
}

export interface Order {
  id: string;
  status: string;
  lines: OrderLine[];
  subtotal: Money;
  discount: Money;
  tax: Money;
  total: Money;
  payment_id: string;
  cancel_reason: string;
  created_at: string;
}

export interface CheckoutResponse {
  order_id: string;
  status: string;
  payment_id: string;
  payment_client_secret: string;
  total: Money;
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
