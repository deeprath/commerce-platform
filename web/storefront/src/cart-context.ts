import { createContext, useContext } from "react";
import type { CartView } from "./types";

export interface CartCtx {
  cart: CartView | null;
  count: number;
  loading: boolean;
  refresh: (coupon?: string) => Promise<void>;
  add: (productId: string, qty?: number) => Promise<void>;
  setQty: (productId: string, qty: number) => Promise<void>;
  remove: (productId: string) => Promise<void>;
  clear: () => Promise<void>;
}

export const Ctx = createContext<CartCtx | null>(null);

export function useCart(): CartCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error("useCart must be used within CartProvider");
  return c;
}
