import { useEffect, useState } from "react";
import { api } from "../api.js";

export default function Planner({ me }) {
  const [tracks, setTracks] = useState([]);
  const [notes, setNotes] = useState([]);
  const [reminders, setReminders] = useState([]);
  const [deliveries, setDeliveries] = useState([]);
  const [trackTitle, setTrackTitle] = useState("");
  const [noteBody, setNoteBody] = useState("");
  const [assist, setAssist] = useState("Tomorrow 9am remind me to review onboarding notes");
  const [remTitle, setRemTitle] = useState("Review Atlas deploy");
  const [dueAt, setDueAt] = useState("");
  const [err, setErr] = useState("");
  const [assistOut, setAssistOut] = useState(null);

  async function load() {
    const [t, n, r, d] = await Promise.all([api.tracks(), api.notes(), api.reminders(), api.deliveries()]);
    setTracks(t.tracks || []);
    setNotes(n.notes || []);
    setReminders(r.reminders || []);
    setDeliveries(d.deliveries || []);
  }

  useEffect(() => {
    const inOneMin = new Date(Date.now() + 60 * 1000).toISOString().slice(0, 16);
    setDueAt(inOneMin);
    load().catch((e) => setErr(e.message));
  }, []);

  async function addTrack(e) {
    e.preventDefault();
    await api.createTrack(trackTitle);
    setTrackTitle("");
    await load();
  }

  async function addNote(e) {
    e.preventDefault();
    await api.createNote({ body: noteBody });
    setNoteBody("");
    await load();
  }

  async function addReminder(e) {
    e.preventDefault();
    await api.createReminder({
      title: remTitle,
      body: "Created from the planner UI",
      due_at: new Date(dueAt).toISOString(),
      channel: "email",
      destination: me?.email || "",
    });
    await load();
  }

  async function runAssist(e) {
    e.preventDefault();
    setErr("");
    try {
      const out = await api.assist({ text: assist, destination: me?.email });
      setAssistOut(out);
      await load();
    } catch (e) {
      setErr(e.message);
    }
  }

  return (
    <div className="stack">
      <h1>Planner</h1>
      {err && <div className="error">{err}</div>}
      <form className="card" onSubmit={runAssist}>
        <header>
          <h2>LLM assist</h2>
        </header>
        <textarea value={assist} onChange={(e) => setAssist(e.target.value)} />
        <button style={{ marginTop: 12 }}>Turn this into tracks and reminders</button>
        {assistOut && <p className="ok">Applied.</p>}
      </form>
      <div className="grid-2">
        <form className="card" onSubmit={addTrack}>
          <h2>Tracks</h2>
          <input value={trackTitle} onChange={(e) => setTrackTitle(e.target.value)} placeholder="Name" />
          <button style={{ marginTop: 12 }}>Add</button>
          <ul className="list">
            {tracks.map((t) => <li key={t.id}><span>{t.title}<div className="meta">{t.status}</div></span></li>)}
          </ul>
        </form>
        <form className="card" onSubmit={addNote}>
          <h2>Notes</h2>
          <textarea value={noteBody} onChange={(e) => setNoteBody(e.target.value)} />
          <button style={{ marginTop: 12 }}>Add</button>
          <ul className="list">
            {notes.map((n) => <li key={n.id}><span>{n.body}</span></li>)}
          </ul>
        </form>
      </div>
      <div className="grid-2">
        <form className="card" onSubmit={addReminder}>
          <h2>Reminder</h2>
          <label>Title</label>
          <input value={remTitle} onChange={(e) => setRemTitle(e.target.value)} />
          <label>Due</label>
          <input type="datetime-local" value={dueAt} onChange={(e) => setDueAt(e.target.value)} />
          <button style={{ marginTop: 12 }}>Schedule</button>
          <ul className="list">
            {reminders.map((r) => (
              <li key={r.id}>
                <span>{r.title}<div className="meta">{new Date(r.due_at).toLocaleString()} · {r.fired_at ? "fired" : "pending"}</div></span>
              </li>
            ))}
          </ul>
        </form>
        <div className="card">
          <h2>Deliveries</h2>
          <ul className="list">
            {deliveries.map((d) => (
              <li key={d.id}>
                <span>{d.subject || d.channel}<div className="meta">{d.status} · {d.destination}</div></span>
              </li>
            ))}
            {deliveries.length === 0 && <li><span className="meta">None yet.</span></li>}
          </ul>
        </div>
      </div>
    </div>
  );
}
