import { useEffect, useState } from "react";
import { NavLink, Route, Routes, useNavigate } from "react-router-dom";
import { api, setUnauthorizedHandler } from "./api";
import { Login } from "./pages/Login";
import { Products } from "./pages/Products";
import { Orders } from "./pages/Orders";
import { OrderDetail } from "./pages/OrderDetail";
import { Returns } from "./pages/Returns";
import { Shipments } from "./pages/Shipments";

const NAV = [
  { to: "/products", label: "Products" },
  { to: "/orders", label: "Orders" },
  { to: "/returns", label: "Returns" },
  { to: "/shipments", label: "Shipments" },
];

function Shell({ onSignOut, children }: Readonly<{ onSignOut: () => void; children: React.ReactNode }>) {
  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">◆ Commerce Admin</div>
        <nav>
          {NAV.map((n) => (
            <NavLink key={n.to} to={n.to} className={({ isActive }) => (isActive ? "active" : "")}>
              {n.label}
            </NavLink>
          ))}
        </nav>
        <button className="linkbtn signout" onClick={onSignOut}>
          Sign out
        </button>
      </aside>
      <main className="content">{children}</main>
    </div>
  );
}

export function App() {
  const nav = useNavigate();
  const [authed, setAuthed] = useState(() => localStorage.getItem("admin_signed_in") === "1");

  useEffect(() => {
    setUnauthorizedHandler(() => {
      localStorage.removeItem("admin_signed_in");
      setAuthed(false);
    });
    return () => setUnauthorizedHandler(null);
  }, []);

  function onAuthed() {
    localStorage.setItem("admin_signed_in", "1");
    setAuthed(true);
    nav("/orders");
  }
  async function signOut() {
    try {
      await api.logout();
    } catch {
      /* ignore */
    }
    localStorage.removeItem("admin_signed_in");
    setAuthed(false);
  }

  if (!authed) return <Login onAuthed={onAuthed} />;

  return (
    <Shell onSignOut={signOut}>
      <Routes>
        <Route path="/products" element={<Products />} />
        <Route path="/orders" element={<Orders />} />
        <Route path="/orders/:id" element={<OrderDetail />} />
        <Route path="/returns" element={<Returns />} />
        <Route path="/shipments" element={<Shipments />} />
        <Route path="*" element={<Orders />} />
      </Routes>
    </Shell>
  );
}
