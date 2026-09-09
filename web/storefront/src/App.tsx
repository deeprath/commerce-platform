import { useState } from "react";
import { Link, Route, Routes } from "react-router-dom";
import { Browse } from "./pages/Browse";
import { Product } from "./pages/Product";
import { Login } from "./pages/Login";
import { Cart } from "./pages/Cart";
import { Checkout } from "./pages/Checkout";
import { Orders, OrderDetail } from "./pages/Orders";
import { api } from "./api";
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

export function App() {
  const [authed, setAuthed] = useState<boolean>(() => localStorage.getItem("signed_in") === "1");

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
