import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api, setToken } from "../api.js";

export default function Register() {
  const nav = useNavigate();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState("");

  async function onSubmit(e) {
    e.preventDefault();
    setErr("");
    try {
      const out = await api.register({ name, email, password });
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
        <h1>Create account</h1>
        <p>The first person here becomes admin.</p>
        <label htmlFor="register-name">Name</label>
        <input id="register-name" value={name} onChange={(e) => setName(e.target.value)} />
        <label htmlFor="register-email">Email</label>
        <input id="register-email" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} required />
        <label htmlFor="register-password">Password</label>
        <input id="register-password" type="password" autoComplete="new-password" minLength={8} value={password} onChange={(e) => setPassword(e.target.value)} required />
        {err && <div className="error">{err}</div>}
        <button style={{ marginTop: 16, width: "100%" }}>Create account</button>
        <p style={{ marginTop: 12 }}>Already registered? <Link to="/login">Sign in</Link></p>
      </form>
    </div>
  );
}
