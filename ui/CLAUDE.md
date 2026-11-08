# UI (Vite + React)

## Commands (run from `ui/`)

```bash
npm ci            # install exactly what package-lock.json records
npm run dev       # Vite on :5173
npm run build     # production build, and the only automated check
npm run preview
```

There is no lint, format, or test script.

**Dev server limits**: the Vite proxy in `vite.config.js` expects services on host ports 8081-8084 and has no `/api/engine` entry, so engine calls and the chat pane fail under `npm run dev`. To test those, use the full stack on `:8080`: `docker compose up --build -d ui`.

## Conventions

- **Stack**: JavaScript and JSX, React 18, React Router 6. No TypeScript, state library, data-fetching library, or CSS framework.
- **Style**: double quotes, semicolons, 2-space indent, explicit `.js`/`.jsx` extensions on local imports.
- **Components**: function components with `export default function Name`. Routed screens live in `pages/`, reusable pieces in `components/`, and small file-private helpers at the bottom of the file.
- **API calls**: add a method to the `api` object in `src/api.js`. Components never call `fetch`. The chat socket in `KnowledgeChat` is the one direct connection.
- **Responses**: list endpoints return a named key, read as `out.tracks || []`.
- **Auth token**: only through `getToken` and `setToken`.
- **Routing**: top-level routes in `App.jsx`; `/app/*` routes nest inside `Workspace`, guarded by `Private`.
- **Shared data**: `Workspace` fetches `me` and passes it down as a prop. There is no React context.
- **Styling**: `className` with `src/styles.css`. Inline `style` only for one-off spacing.
- **Forms**: `onSubmit` with `preventDefault`. Non-submit buttons set `type="button"`.
- **Accessibility**: icon-only buttons need an `aria-label`.

## Error handling

- Keep a local `err` state and render `{err && <div className="error">{err}</div>}`.
- Anything the user triggers gets `try/catch` with `setErr(e.message)`.
- A silent `catch` is only for background loads, such as refreshing the chat list.
- `Workspace` treats a failed `api.me()` as signed out: it clears the token and redirects.

## Chat pane

- `KnowledgeChat` is a sibling of the routes, mounted from `Workspace` only while open. Its session id is React state, not a URL.
- The socket effect intentionally has empty dependencies, so navigation doesn't reconnect. It reconnects 800ms after a drop.
- Socket handlers read current values through refs because they outlive any single render.
- Existing `window` events `atlas-chats-changed` and `atlas-open-chat` stay as they are. Don't add more; use props or lifted state.

## React rules

- **One responsibility**: extract a component when a file takes on a second concern. Don't grow `KnowledgeChat`.
- **State placement**: keep state in the lowest common owner. Derive values during render instead of storing them, and don't mirror props into state.
- **Effects**: only for synchronizing with something external, such as a network load, socket, or DOM listener. User-driven logic belongs in event handlers.
- **Cleanup**: every effect that subscribes or starts async work cleans up. Use a `cancelled` flag for async loads, as `KnowledgeChat` does.
- **Updates**: immutable, with functional `setState` when the next value depends on the previous one.
- **Keys**: stable ids. Index-based keys are only for lists without ids, such as streamed chat messages.
- **Memoization**: `useMemo` and `useCallback` aren't used anywhere. Add them only for a measured problem.

## Verify before done

1. `npm run build` succeeds.
2. With no automated tests, exercise the changed screen on the full stack (`:8080`), including its error state.
3. If an API payload changed, confirm the Go struct accepts it, since unknown fields are rejected.

## Suggested improvements

- No linter or formatter (for example ESLint and Prettier).
- No test runner (for example Vitest).
- `ui/Dockerfile` runs `npm install`; `npm ci` would fail on a stale lockfile instead of updating it.
- `Planner.jsx` `addTrack`, `addNote`, and `addReminder` don't catch errors.
