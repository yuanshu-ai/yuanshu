# Personal Web workbench (Codex-first)

Yuanshu Server exposes three separate products through one process and certificate:

- `/` — personal Codex workbench;
- `/admin` — same-origin Server administration;
- `/pair` — browser pairing.

The workbench is task-first. On phones it provides Home, Tasks, Notifications, and Settings navigation; Thread details open full-screen. Desktop uses Node/workspace context, task summaries, and Thread detail columns. Codex is the current production Adapter; future Agents will use the same task-oriented shell with controls gated by runtime capabilities.

## Task flow

- Restore the non-exportable browser identity and known Node bindings from IndexedDB.
- Synchronize Node/workspace/Thread summaries without loading all Thread bodies.
- Open a Thread to request its stable history snapshot and merge later events by sequence.
- Create a Thread, append a Turn, steer active work, or interrupt it through explicitly signed controls.
- Use a Thread lease before Turn changes or approval decisions; reconnect never automatically reacquires or steals control.

The Timeline renders user/Agent messages, commands, tools, errors, file changes, and bounded Diffs. Markdown does not execute raw HTML, load remote images, or allow script URLs. Diffs are fetched on demand through the Node's logical workspace boundary and remain capped.

## Recovery and storage

Browser connection settings, non-exportable control identity, Node bindings, event cursors, and control sequences live in IndexedDB. Prompt text, Thread bodies, command output, Diffs, and drafts do not persist there, in localStorage, or in URLs.

Node events are replayed from a cursor after reconnect. Duplicates are ignored, gaps trigger snapshot recovery, and side-effecting controls are never resent automatically. `unknown` and `ambiguous` results remain visible.

## Current boundary

The current interface is a responsive Web application, not yet an installable PWA. Web Push, system notifications, attachments, archival, favorites, team ACLs, and additional Agent adapters are later work. Before a second Adapter is added, the Node will gain Agent instances, stable Yuanshu task bindings, and runtime capability negotiation. Automated Chromium/WebKit viewport tests do not replace PF-052 real Safari, Android Chrome, iPad, and network-switching acceptance.
