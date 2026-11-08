import { NavLink, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { api, setToken } from "../api.js";
import Knowledge from "./Knowledge.jsx";
import KnowledgeChat from "./KnowledgeChat.jsx";
import Planner from "./Planner.jsx";
import Account from "./Account.jsx";
import ChatIcon from "../components/ChatIcon.jsx";

function roleLabel(roles) {
  if (!roles?.length) return "";
  const quiet = roles.filter((r) => r !== "admin");
  return (quiet[0] || roles[0]);
}

export default function Workspace() {
  const [me, setMe] = useState(null);
  const [chats, setChats] = useState([]);
  const [navOpen, setNavOpen] = useState(false);
  const [chatOpen, setChatOpen] = useState(false);
  const [chatId, setChatId] = useState("");
  const [optionsOpen, setOptionsOpen] = useState(false);
  const optionsRef = useRef(null);
  const nav = useNavigate();
  const loc = useLocation();

  useEffect(() => {
    api.me().then(setMe).catch(() => {
      setToken("");
      nav("/login");
    });
  }, [nav]);

  async function loadChats() {
    try {
      const out = await api.engineChats();
      setChats(out.chats || []);
    } catch {
      setChats([]);
    }
  }

  useEffect(() => {
    setNavOpen(false);
    setOptionsOpen(false);
  }, [loc.pathname]);

  useEffect(() => {
    if (!optionsOpen) return;
    function onDoc(e) {
      if (!optionsRef.current?.contains(e.target)) setOptionsOpen(false);
    }
    function onKey(e) {
      if (e.key === "Escape") setOptionsOpen(false);
    }
    document.addEventListener("mousedown", onDoc);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDoc);
      document.removeEventListener("keydown", onKey);
    };
  }, [optionsOpen]);

  useEffect(() => {
    const match = loc.pathname.match(/\/knowledge\/chat(?:\/([^/]+))?\/?$/);
    if (!match) return;
    setChatOpen(true);
    setChatId(match[1] || "");
    nav("/app/knowledge", { replace: true });
  }, [loc.pathname, nav]);

  useEffect(() => {
    const open = () => setChatOpen(true);
    window.addEventListener("atlas-open-chat", open);
    return () => window.removeEventListener("atlas-open-chat", open);
  }, []);

  useEffect(() => {
    loadChats();
    const onChange = () => loadChats();
    window.addEventListener("atlas-chats-changed", onChange);
    return () => window.removeEventListener("atlas-chats-changed", onChange);
  }, [loc.pathname]);

  function logout() {
    setToken("");
    nav("/");
  }

  return (
    <>
      <header className="topbar">
        <div className="brand-row">
          <button type="button" className="ghost menu-btn" aria-label="Open navigation" onClick={() => setNavOpen((v) => !v)}>
            Menu
          </button>
          <Link className="brand" to="/">Atlas</Link>
        </div>
        <div className="nav">
          <span className="meta">{me ? `${me.name} · ${roleLabel(me.roles)}` : "…"}</span>
          <button
            type="button"
            className={`ghost icon-btn${chatOpen ? " on" : ""}`}
            aria-label="Chat"
            title="Chat"
            onClick={() => setChatOpen((v) => !v)}
          >
            <ChatIcon size={18} />
          </button>
          <div className="options" ref={optionsRef}>
            <button
              type="button"
              className={`ghost icon-btn${optionsOpen || loc.pathname.startsWith("/app/account") ? " on" : ""}`}
              aria-label="Options"
              aria-expanded={optionsOpen}
              aria-haspopup="menu"
              onClick={() => setOptionsOpen((v) => !v)}
            >
              <OptionsIcon />
            </button>
            {optionsOpen && (
              <div className="options-menu" role="menu">
                <NavLink role="menuitem" to="/app/account">Account</NavLink>
                <button type="button" role="menuitem" className="options-signout" onClick={logout}>Sign out</button>
              </div>
            )}
          </div>
        </div>
      </header>
      <div className={`workspace${chatOpen ? " chat-open" : ""}${navOpen ? " nav-open" : ""}`}>
        <nav className="side">
          <NavLink to="/app" end>Overview</NavLink>
          <NavLink to="/app/knowledge" end>Knowledge</NavLink>
          <NavLink to="/app/planner">Planner</NavLink>
        </nav>
        <div className="main" onClick={() => navOpen && setNavOpen(false)}>
          <Routes>
            <Route index element={<Overview me={me} />} />
            <Route path="knowledge" element={<Knowledge />} />
            <Route path="planner" element={<Planner me={me} />} />
            <Route path="account" element={<Account me={me} />} />
          </Routes>
        </div>
        {chatOpen && (
          <aside className="chat-pane" aria-label="Chat">
            <KnowledgeChat
              chatId={chatId}
              sessions={chats}
              onChatId={setChatId}
              onClose={() => setChatOpen(false)}
            />
          </aside>
        )}
      </div>
    </>
  );
}

function OptionsIcon({ size = 18 }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
      <circle cx="12" cy="5" r="1.7" />
      <circle cx="12" cy="12" r="1.7" />
      <circle cx="12" cy="19" r="1.7" />
    </svg>
  );
}

function accessChips(perms) {
  const labels = [];
  if (perms?.some((p) => p.startsWith("knowledge"))) labels.push("knowledge");
  if (perms?.some((p) => p.startsWith("planner"))) labels.push("planner");
  if (perms?.some((p) => p.startsWith("notify"))) labels.push("notify");
  return labels;
}

function Overview({ me }) {
  const chips = accessChips(me?.perms);
  return (
    <div className="stack">
      <h1>Workspace</h1>
      <p>Welcome back. Your notes, conversations, and reminders live here.</p>
      <div className="grid-2">
        <div className="card">
          <h2>Your access</h2>
          <div className="chips">
            {chips.map((c) => <span key={c} className="chip">{c}</span>)}
            {chips.length === 0 && <span className="meta">Loading</span>}
          </div>
        </div>
        <div className="card">
          <h2>Suggested path</h2>
          <p>1. Ingest a note.<br />2. Open chat.<br />3. Ask.</p>
        </div>
      </div>
    </div>
  );
}
