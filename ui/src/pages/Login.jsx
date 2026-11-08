import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api, setToken } from "../api.js";

export default function Login() {
  const nav = useNavigate();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState("");

  async function onSubmit(e) {
    e.preventDefault();
    setErr("");
    try {
      const out = await api.login({ email, password });
      setToken(out.token);
      nav("/app");
    } catch (e) {
      setErr(e.message);
    }
  }

  return (
    <div className="auth-screen">
      <div className="auth-brand">
        <Link className="brand" to="/login">Atlas</Link>
      </div>
      <form className="auth-panel" onSubmit={onSubmit}>
        <h1>Sign in</h1>
        <label htmlFor="login-email">Email</label>
        <input id="login-email" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} required />
        <label htmlFor="login-password">Password</label>
        <input id="login-password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        {err && <div className="error">{err}</div>}
        <button style={{ marginTop: 16, width: "100%" }}>Continue</button>
        <p style={{ marginTop: 12 }}>No account? <Link to="/register">Create account</Link></p>
      </form>
    </div>
  );
}
