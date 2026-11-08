import { useEffect, useRef, useState } from "react";
import { api, getToken } from "../api.js";
import ChatIcon from "../components/ChatIcon.jsx";

function engineSocket() {
  const proto = window.location.protocol === "https:" ? "wss" : "ws";
  const token = encodeURIComponent(getToken());
  return new WebSocket(`${proto}://${window.location.host}/api/engine/v1/ws?token=${token}`);
}

export default function KnowledgeChat({ chatId, sessions, onChatId, onClose }) {
  const [chat, setChat] = useState(null);
  const [draft, setDraft] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [sources, setSources] = useState([]);
  const [providers, setProviders] = useState([]);
  const [provider, setProvider] = useState("");
  const [sessionsOpen, setSessionsOpen] = useState(false);
  const wsRef = useRef(null);
  const chatRef = useRef(null);
  const providerRef = useRef(provider);
  const onChatIdRef = useRef(onChatId);
  const suppressLoad = useRef(false);
  const bottom = useRef(null);

  useEffect(() => { chatRef.current = chat; }, [chat]);
  useEffect(() => { providerRef.current = provider; }, [provider]);
  useEffect(() => { onChatIdRef.current = onChatId; }, [onChatId]);

  useEffect(() => {
    api.engineProviders().then((out) => {
      const list = out.providers || [];
      setProviders(list);
      setProvider((cur) => (list.some((p) => p.id === cur) ? cur : (list[0]?.id || "")));
    }).catch(() => {
      setProviders([]);
      setProvider("");
    });
  }, []);

  useEffect(() => {
    if (suppressLoad.current) {
      suppressLoad.current = false;
      return;
    }
    let cancelled = false;
    async function load() {
      setErr("");
      setBusy(false);
      if (!chatId) {
        setChat({ id: "", title: "New chat", messages: [] });
        setSources([]);
        return;
      }
      if (chatRef.current?.id === chatId) return;
      try {
        const row = await api.engineChat(chatId);
        if (!cancelled) {
          setChat(row);
          setSources([]);
        }
      } catch (e) {
        if (!cancelled) setErr(e.message);
      }
    }
    load();
    return () => { cancelled = true; };
  }, [chatId]);

  useEffect(() => {
    let stopped = false;
    let socket;
    let timer;

    function connect() {
      if (stopped) return;
      const ws = engineSocket();
      socket = ws;
      wsRef.current = ws;
      ws.onmessage = (ev) => {
        let msg;
        try { msg = JSON.parse(ev.data); } catch { return; }
        if (msg.type === "chat" && msg.chat?.id) {
          const nextId = msg.chat.id;
          suppressLoad.current = true;
          setChat((cur) => ({ ...(cur || {}), id: nextId, title: msg.chat.title || cur?.title }));
          onChatIdRef.current?.(nextId);
          window.dispatchEvent(new Event("atlas-chats-changed"));
        }
        if (msg.type === "token" && msg.content) {
          setChat((cur) => appendDelta(cur, msg.content));
        }
        if (msg.type === "done") {
          setBusy(false);
          if (msg.chat) {
            setChat((cur) => ({
              ...(cur || {}),
              ...msg.chat,
              messages: msg.chat.messages?.length ? msg.chat.messages : cur?.messages,
            }));
          }
          setSources(msg.sources || []);
          window.dispatchEvent(new Event("atlas-chats-changed"));
        }
        if (msg.type === "error") {
          setBusy(false);
          setErr(msg.error || "chat failed");
        }
      };
      ws.onclose = () => {
        if (!stopped) timer = setTimeout(connect, 800);
      };
    }

    connect();
    return () => {
      stopped = true;
      clearTimeout(timer);
      socket?.close();
      wsRef.current = null;
    };
  }, []);

  useEffect(() => {
    bottom.current?.scrollIntoView({ behavior: "smooth" });
  }, [chat?.messages]);

  function send(e) {
    e.preventDefault();
    const text = draft.trim();
    if (!text || busy) return;
    setErr("");
    setBusy(true);
    setDraft("");
    setChat((cur) => ({
      ...(cur || { title: "New chat", messages: [] }),
      messages: [...(cur?.messages || []), { role: "user", content: text }, { role: "assistant", content: "" }],
    }));
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      setBusy(false);
      setErr("engine is not connected");
      return;
    }
    if (!providerRef.current) {
      setBusy(false);
      setErr("Choose a provider");
      return;
    }
    ws.send(JSON.stringify({
      type: "send",
      chat_id: chatRef.current?.id || chatId || "",
      content: text,
      provider: providerRef.current,
    }));
  }

  const messages = chat?.messages || [];

  return (
    <div className="chat-shell">
      <header className="chat-head">
        <div className="chat-title-row">
          <h1>{chat?.title || "Chat"}</h1>
          <div className="chat-title-actions">
            <button type="button" className="ghost" onClick={() => setSessionsOpen((v) => !v)} aria-expanded={sessionsOpen}>
              Sessions
            </button>
            <button type="button" className="ghost icon-btn" aria-label="Close" onClick={onClose}>×</button>
          </div>
        </div>
        <div className={`session-rail${sessionsOpen ? " open" : ""}`}>
          <button type="button" className={!chatId ? "on" : ""} onClick={() => onChatId("")}>New chat</button>
          {(sessions || []).map((s) => (
            <button key={s.id} type="button" className={s.id === chatId ? "on" : ""} onClick={() => onChatId(s.id)}>
              {s.title || "Untitled"}
            </button>
          ))}
        </div>
      </header>
      <div className="chat-log">
        {messages.length === 0 && <p className="meta">Ask about a note you stored. When something matches, the answer names the note it used.</p>}
        {messages.map((m, i) => (
          <div key={`${m.role}-${i}`} className={`bubble ${m.role === "user" ? "user" : "bot"}`}>{m.content}</div>
        ))}
        <div ref={bottom} />
      </div>
      {sources.length > 0 && (
        <div className="sources">Used: {sources.map((s) => s.title).filter(Boolean).join(" · ")}</div>
      )}
      {err && <div className="error">{err}</div>}
      <div className="chat-foot">
        <label className="chat-provider">
          Provider
          <select value={provider} onChange={(e) => setProvider(e.target.value)}>
            {providers.length === 0 && <option value="">No provider</option>}
            {providers.map((p) => <option key={p.id} value={p.id}>{p.id}</option>)}
          </select>
        </label>
      <form className="chat-compose" onSubmit={send}>
        <textarea
          rows={2}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="Message Atlas…"
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) send(e);
          }}
        />
        <button className="send-in" type="submit" aria-label="Send" disabled={busy || !draft.trim() || !provider}>
          <ChatIcon size={16} />
        </button>
      </form>
      </div>
    </div>
  );
}

function appendDelta(cur, delta) {
  const messages = [...(cur?.messages || [])];
  if (messages.length === 0 || messages[messages.length - 1].role !== "assistant") {
    messages.push({ role: "assistant", content: delta });
  } else {
    const last = messages[messages.length - 1];
    messages[messages.length - 1] = { ...last, content: (last.content || "") + delta };
  }
  return { ...(cur || {}), messages };
}
