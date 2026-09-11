import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import type { ShopStaff } from "../types";
import { SellerNav } from "../components/SellerNav";

export function SellerStaff({ authed }: Readonly<{ authed: boolean }>) {
  const [staff, setStaff] = useState<ShopStaff | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [subject, setSubject] = useState("");
  const [busy, setBusy] = useState(false);

  function load() {
    api
      .listShopStaff()
      .then(setStaff)
      .catch((e) => setErr(String(e.message ?? e)));
  }

  useEffect(() => {
    if (!authed) return;
    load();
  }, [authed]);

  if (!authed)
    return (
      <p className="muted">
        Please <Link to="/login">sign in</Link>.
      </p>
    );

  async function add(e: React.FormEvent) {
    e.preventDefault();
    if (!subject.trim()) return;
    setBusy(true);
    setErr(null);
    try {
      await api.addShopStaff(subject.trim());
      setSubject("");
      load();
    } catch (e) {
      setErr(String((e as { message?: string }).message ?? e));
    } finally {
      setBusy(false);
    }
  }

  async function remove(sub: string) {
    if (!confirm(`Remove staff access for ${sub}?`)) return;
    await api.removeShopStaff(sub);
    load();
  }

  return (
    <div className="seller">
      <h1>Seller dashboard</h1>
      <SellerNav />

      <h2>Staff</h2>
      <p className="muted small">
        Staff can manage this shop's products but cannot change shop settings. Add someone by their
        account subject id.
      </p>

      {err && <p className="error">{err}</p>}
      {!staff && !err && <p className="muted">Loading…</p>}

      {staff && (
        <ul className="staff-list">
          <li>
            <code>{staff.owner_subject}</code> <span className="muted small">(owner)</span>
          </li>
          {staff.staff_subjects.map((s) => (
            <li key={s}>
              <code>{s}</code>
              <button className="linkbtn" onClick={() => remove(s)}>
                Remove
              </button>
            </li>
          ))}
        </ul>
      )}

      <form onSubmit={add} className="seller-form staff-form">
        <label>
          Add staff (subject id){" "}
          <input value={subject} onChange={(e) => setSubject(e.target.value)} placeholder="Keycloak subject" />
        </label>
        <button type="submit" disabled={busy || !subject.trim()}>
          {busy ? "Adding…" : "Add"}
        </button>
      </form>
    </div>
  );
}
