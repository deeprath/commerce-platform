import { useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { api } from "./api";
import type { CartView } from "./types";
import { Ctx } from "./cart-context";
import type { CartCtx } from "./cart-context";
import { track, toMinor } from "./track";

export function CartProvider({ children }: Readonly<{ children: ReactNode }>) {
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

  const add = useCallback(async (id: string, qty = 1) => {
    const next = await api.addToCart(id, qty);
    setCart(next);
    track("add_to_cart", { product_id: id, value_minor: toMinor(next.total) });
  }, []);

  const setQty = useCallback(async (id: string, qty: number) => {
    setCart(await api.setCartQuantity(id, qty));
  }, []);

  const remove = useCallback(async (id: string) => {
    setCart(await api.removeFromCart(id));
  }, []);

  const clear = useCallback(async () => {
    setCart(await api.clearCart());
  }, []);

  // Memoized so consumers only re-render when something in the value actually
  // changed, not on every CartProvider render.
  const value: CartCtx = useMemo(
    () => ({ cart, count: cart?.total_quantity ?? 0, loading, refresh, add, setQty, remove, clear }),
    [cart, loading, refresh, add, setQty, remove, clear],
  );

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
