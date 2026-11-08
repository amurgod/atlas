const TOKEN_KEY = "atlas_token";

export function getToken() {
  return localStorage.getItem(TOKEN_KEY) || "";
}

export function setToken(token) {
  if (token) localStorage.setItem(TOKEN_KEY, token);
  else localStorage.removeItem(TOKEN_KEY);
}

async function request(path, { method = "GET", body, auth = true } = {}) {
  const headers = { "Content-Type": "application/json" };
  if (auth && getToken()) headers.Authorization = `Bearer ${getToken()}`;
  const res = await fetch(path, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(data.error || res.statusText);
    err.status = res.status;
    throw err;
  }
  return data;
}

export const api = {
  register: (body) => request("/api/user/v1/auth/register", { method: "POST", body, auth: false }),
  login: (body) => request("/api/user/v1/auth/login", { method: "POST", body, auth: false }),
  me: () => request("/api/user/v1/me"),
  updateMe: (body) => request("/api/user/v1/me", { method: "PATCH", body }),
  users: () => request("/api/user/v1/users"),
  addRole: (id, role) => request(`/api/user/v1/users/${id}/roles`, { method: "POST", body: { role } }),
  documents: () => request("/api/knowledge/v1/documents"),
  ingest: (body) => request("/api/knowledge/v1/documents", { method: "POST", body }),
  getDocument: (id) => request(`/api/knowledge/v1/documents/${id}`),
  deleteDocument: (id) => request(`/api/knowledge/v1/documents/${id}`, { method: "DELETE" }),
  search: (query) => request("/api/knowledge/v1/search", { method: "POST", body: { query } }),
  chat: (body) => request("/api/knowledge/v1/chat", { method: "POST", body }),
  publicChat: (body) => request("/api/knowledge/v1/chat/public", { method: "POST", body }),
  engineAddDoc: (body) => request("/api/engine/v1/adddoc", { method: "POST", body }),
  engineRemoveDoc: (id) => request(`/api/engine/v1/removedoc/${id}`, { method: "DELETE" }),
  engineDocs: () => request("/api/engine/v1/docs"),
  engineDoc: (id) => request(`/api/engine/v1/docs/${id}`),
  engineSearch: (query) => request("/api/engine/v1/search", { method: "POST", body: { query } }),
  engineModels: () => request("/api/engine/v1/models"),
  engineProviders: () => request("/api/engine/v1/providers"),
  engineChats: () => request("/api/engine/v1/chats"),
  engineChat: (id) => request(`/api/engine/v1/chats/${id}`),
  engineCreateChat: (title = "New chat") => request("/api/engine/v1/chats", { method: "POST", body: { title } }),
  engineDeleteChat: (id) => request(`/api/engine/v1/chats/${id}`, { method: "DELETE" }),
  tools: (tool, text) => request("/api/knowledge/v1/tools", { method: "POST", body: { tool, text } }),
  tracks: () => request("/api/planner/v1/tracks"),
  createTrack: (title) => request("/api/planner/v1/tracks", { method: "POST", body: { title } }),
  notes: () => request("/api/planner/v1/notes"),
  createNote: (body) => request("/api/planner/v1/notes", { method: "POST", body }),
  reminders: () => request("/api/planner/v1/reminders"),
  createReminder: (body) => request("/api/planner/v1/reminders", { method: "POST", body }),
  assist: (body) => request("/api/planner/v1/assist", { method: "POST", body }),
  deliveries: () => request("/api/notify/v1/deliveries"),
};
