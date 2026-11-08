import { useEffect, useState } from "react";
import { api } from "../api.js";

export default function Knowledge() {
  const [docs, setDocs] = useState([]);
  const [title, setTitle] = useState("Onboarding notes");
  const [content, setContent] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function load() {
    const out = await api.engineDocs();
    setDocs(out.documents || []);
  }

  useEffect(() => {
    load().catch((e) => setErr(e.message));
  }, []);

  async function ingest(e) {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      await api.engineAddDoc({ title, content, source: "paste" });
      setContent("");
      await load();
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  }

  async function remove(id) {
    await api.engineRemoveDoc(id);
    await load();
  }

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Knowledge</h1>
      </div>
      <form className="card" onSubmit={ingest}>
        <h2>Ingest</h2>
        <label>Title</label>
        <input value={title} onChange={(e) => setTitle(e.target.value)} required />
        <label>Content</label>
        <textarea value={content} onChange={(e) => setContent(e.target.value)} required placeholder="Paste a note to store and embed…" />
        {err && <div className="error">{err}</div>}
        <button disabled={busy} style={{ marginTop: 12 }}>{busy ? "Embedding…" : "Store + embed"}</button>
      </form>
      <div className="card">
        <h2>Documents</h2>
        <ul className="list">
          {docs.map((d) => (
            <li key={d.id}>
              <span>{d.title}<div className="meta">{d.source}</div></span>
              <button type="button" onClick={() => remove(d.id)}>Delete</button>
            </li>
          ))}
          {docs.length === 0 && <li><span className="meta">Nothing ingested yet.</span></li>}
        </ul>
      </div>
    </div>
  );
}
