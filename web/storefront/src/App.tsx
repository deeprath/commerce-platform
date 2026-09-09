import { useState } from "react";
import { Link, Route, Routes } from "react-router-dom";
import { Browse } from "./pages/Browse";
import { Product } from "./pages/Product";
import { Login } from "./pages/Login";
import { api } from "./api";

export function App() {
  // The auth cookie is httpOnly so the SPA can't read it; we track a local
  // "signed in" hint for the header only.
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
    <>
      <header className="site-header">
        <Link to="/" className="brand">
          ◆ Commerce
        </Link>
        <nav>
          {authed ? (
            <button className="linkbtn" onClick={signOut}>
              Sign out
            </button>
          ) : (
            <Link to="/login">Sign in</Link>
          )}
        </nav>
      </header>
      <main className="site-main">
        <Routes>
          <Route path="/" element={<Browse />} />
          <Route path="/p/:slug" element={<Product />} />
          <Route path="/login" element={<Login onAuthed={onAuthed} />} />
          <Route path="*" element={<p className="muted">Not found.</p>} />
        </Routes>
      </main>
      <footer className="site-footer">
        <span>Commerce Platform · Phase 1 storefront</span>
      </footer>
    </>
  );
}
