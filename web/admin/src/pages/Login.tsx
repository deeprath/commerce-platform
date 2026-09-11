import { useState } from "react";
import { api } from "../api";

export function Login({ onAuthed }: Readonly<{ onAuthed: () => void }>) {
  const [username, setUsername] = useState("adminuser");
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
    } catch (e) {
      const reason = (e as { info?: { reason?: string } }).info?.reason;
      setErr(reason === "BAD_CREDENTIALS" ? "Invalid username or password." : "Sign-in failed. Try again.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login">
      <h1>◆ Commerce Admin</h1>
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
      <p className="muted small">
        Needs an operator role. Local dev: adminuser / adminuser123
      </p>
    </div>
  );
}
