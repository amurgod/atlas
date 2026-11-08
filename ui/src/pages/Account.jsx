import { useEffect, useState } from "react";
import { api } from "../api.js";

export default function Account({ me }) {
  const [name, setName] = useState(me?.name || "");
  const [users, setUsers] = useState([]);
  const [msg, setMsg] = useState("");
  const isAdmin = me?.roles?.includes("admin");

  useEffect(() => {
    setName(me?.name || "");
    if (isAdmin) {
      api.users().then((o) => setUsers(o.users || [])).catch(() => {});
    }
  }, [me, isAdmin]);

  async function save(e) {
    e.preventDefault();
    await api.updateMe({ name });
    setMsg("Saved.");
  }

  async function grant(id) {
    await api.addRole(id, "admin");
    const o = await api.users();
    setUsers(o.users || []);
  }

  return (
    <div className="stack">
      <h1>Account</h1>
      <form className="card" onSubmit={save} style={{ maxWidth: 420 }}>
        <label>Display name</label>
        <input value={name} onChange={(e) => setName(e.target.value)} />
        <label>Email</label>
        <p className="meta">{me?.email}</p>
        {msg && <div className="ok">{msg}</div>}
        <button style={{ marginTop: 12 }}>Save</button>
      </form>
      {isAdmin && (
        <div className="card">
          <h2>Users</h2>
          <ul className="list">
            {users.map((u) => (
              <li key={u.id}>
                <span>{u.email}<div className="meta">{u.roles.join(", ")}</div></span>
                {!u.roles.includes("admin") && (
                  <button type="button" className="lavender" onClick={() => grant(u.id)}>Grant admin</button>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
