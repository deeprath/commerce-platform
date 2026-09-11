// Shapes returned by the BFF (protojson: snake_case fields, string enums,
// int64 as string).

export interface ApiError {
  status: number;
  code: string;
  reason: string;
}

export interface Money {
  currency_code: string;
  units: string;
  nanos: number;
}

export function money(m?: Money | null): string {
  if (!m) return "—";
  const amount = Number(m.units ?? 0) + Math.round((m.nanos ?? 0) / 1e7) / 100;
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency: m.currency_code || "USD" }).format(amount);
  } catch {
    return `${m.currency_code} ${amount.toFixed(2)}`;
  }
}

export function when(s?: string): string {
  if (!s) return "—";
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString();
}

// --- catalog ---
export interface Product {
  id: string;
  slug: string;
  title: string;
  description: string;
  category_id: string;
  list_price: Money;
  media_keys: string[];
  status: string; // PRODUCT_STATUS_*
  attributes: Record<string, string>;
}

// --- orders ---
export interface OrderLine {
  product_id: string;
  title: string;
  quantity: number;
  unit_price: Money;
  line_total: Money;
}

export interface Order {
  id: string;
  owner_id: string;
  status: string; // ORDER_STATUS_*
  lines: OrderLine[];
  subtotal: Money;
  discount: Money;
  tax: Money;
  total: Money;
  payment_id: string;
  cancel_reason: string;
  created_at: string;
  updated_at: string;
}

// --- returns ---
export interface ReturnLine {
  product_id: string;
  quantity: number;
  refund_amount: Money;
}

export interface Return {
  id: string;
  order_id: string;
  owner_id: string;
  status: string; // RETURN_STATUS_*
  reason: string;
  lines: ReturnLine[];
  refund_total: Money;
  decided_by: string;
  decision_note: string;
  created_at: string;
  updated_at: string;
}

// --- shipments ---
export interface ShipmentItem {
  product_id: string;
  title: string;
  quantity: number;
}

export interface Shipment {
  id: string;
  order_id: string;
  owner_id: string;
  status: string; // SHIPMENT_STATUS_*
  carrier: string;
  tracking_number: string;
  items: ShipmentItem[];
  created_at: string;
  shipped_at: string;
  delivered_at: string;
  cancel_reason: string;
}

// Strip the proto enum prefix for display: ORDER_STATUS_FULFILLED -> Fulfilled.
export function label(enumValue: string): string {
  const bare = enumValue.replace(/^[A-Z]+_STATUS_/, "").replaceAll("_", " ");
  return bare.charAt(0) + bare.slice(1).toLowerCase();
}
