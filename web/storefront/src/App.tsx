import { useEffect, useState } from "react";
import { Link, Route, Routes, useLocation } from "react-router-dom";
import { track } from "./track";
import { Browse } from "./pages/Browse";
import { Product } from "./pages/Product";
import { Shop } from "./pages/Shop";
import { Login } from "./pages/Login";
import { Cart } from "./pages/Cart";
import { Checkout } from "./pages/Checkout";
import { Orders, OrderDetail } from "./pages/Orders";
import { api, setUnauthorizedHandler } from "./api";
import { CartProvider } from "./cart";
import { useCart } from "./cart-context";

function Header({ authed, onSignOut }: { authed: boolean; onSignOut: () => void }) {
  const { count } = useCart();
  return (
    <header className="site-header">
      <Link to="/" className="brand">
        ◆ Commerce
      </Link>
      <nav>
        <Link to="/cart" className="cart-link">
          Cart{count > 0 && <span className="cart-badge">{count}</span>}
        </Link>
        {authed ? (
          <>
            <Link to="/orders">Orders</Link>
            <button className="linkbtn" onClick={onSignOut}>
              Sign out
            </button>
          </>
        ) : (
          <Link to="/login">Sign in</Link>
        )}
      </nav>
    </header>
  );
}

// One clickstream page_view per client-side navigation.
function usePageViews() {
  const { pathname } = useLocation();
  useEffect(() => {
    track("page_view", {
      path: pathname,
      referrer: typeof document !== "undefined" ? document.referrer : "",
    });
  }, [pathname]);
}

export function App() {
  usePageViews();
  const [authed, setAuthed] = useState<boolean>(() => localStorage.getItem("signed_in") === "1");

  // If any request 401s, the session cookie is gone — drop the cached flag so
  // protected pages render their sign-in prompt instead of a raw error.
  useEffect(() => {
    setUnauthorizedHandler(() => {
      localStorage.removeItem("signed_in");
      setAuthed(false);
    });
    return () => setUnauthorizedHandler(null);
  }, []);

  function onAuthed() {
    localStorage.setItem("signed_in", "1");
    setAuthed(true);
  }
  async function signOut() {
    try {
      await api.logout();
    } catch {
      /* ignore */
    }
    localStorage.removeItem("signed_in");
    setAuthed(false);
  }

  return (
    <CartProvider>
      <Header authed={authed} onSignOut={signOut} />
      <main className="site-main">
        <Routes>
          <Route path="/" element={<Browse />} />
          <Route path="/p/:slug" element={<Product />} />
          <Route path="/shops/:slug" element={<Shop />} />
          <Route path="/cart" element={<Cart />} />
          <Route path="/checkout" element={<Checkout authed={authed} />} />
          <Route path="/orders" element={<Orders authed={authed} />} />
          <Route path="/orders/:id" element={<OrderDetail authed={authed} />} />
          <Route path="/login" element={<Login onAuthed={onAuthed} />} />
          <Route path="*" element={<p className="muted">Not found.</p>} />
        </Routes>
      </main>
      <footer className="site-footer">
        <span>Commerce Platform · storefront</span>
      </footer>
    </CartProvider>
  );
}
