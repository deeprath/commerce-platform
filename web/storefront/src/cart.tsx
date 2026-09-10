import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api } from "./api";
import type { CartView } from "./types";
import { Ctx } from "./cart-context";
import type { CartCtx } from "./cart-context";
import { track, toMinor } from "./track";

export function CartProvider({ children }: { children: ReactNode }) {
  const [cart, setCart] = useState<CartView | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async (coupon?: string) => {
    try {
      setCart(await api.getCart(coupon));
    } catch {
      /* leave the last known cart */
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const wrap = (fn: () => Promise<CartView>) => async () => {
    setCart(await fn());
  };

  const add = async (id: string, qty = 1) => {
    const next = await api.addToCart(id, qty);
    setCart(next);
    track("add_to_cart", { product_id: id, value_minor: toMinor(next.total) });
  };

  const value: CartCtx = {
    cart,
    count: cart?.total_quantity ?? 0,
    loading,
    refresh,
    add,
    setQty: (id, qty) => wrap(() => api.setCartQuantity(id, qty))(),
    remove: (id) => wrap(() => api.removeFromCart(id))(),
    clear: () => wrap(() => api.clearCart())(),
  };

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
