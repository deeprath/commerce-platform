import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "../api";

// A React-native login form that posts to the BFF, which brokers to Keycloak
// and sets an httpOnly cookie (docs/ARCHITECTURE.md §7.2, approach A).
export function Login({ onAuthed }: Readonly<{ onAuthed: () => void }>) {
  const nav = useNavigate();
  const [username, setUsername] = useState("testuser");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await api.login(username, password);
      onAuthed();
      nav("/");
    } catch (e) {
      const reason = (e as { info?: { reason?: string } }).info?.reason;
      setErr(reason === "BAD_CREDENTIALS" ? "Invalid username or password." : "Sign-in failed. Try again.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login">
      <h1>Sign in</h1>
      <form onSubmit={submit}>
        <label>
          Username{" "}
          <input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" />
        </label>
        <label>
          Password{" "}
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
          />
        </label>
        {err && <p className="error">{err}</p>}
        <button type="submit" disabled={busy || !password}>
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>
      <p className="muted small">Local dev: testuser / testuser123</p>
    </div>
  );
}
