import { FormEvent, useCallback, useEffect, useState } from "react";
import { approveAction, createSession, getRoutingStatus, interruptTurn, listSessions, sendTurn, SessionUIError, subscribeToSession, type SessionConnection } from "./api";
import type { ApprovalRequest, RoutingStatus, SessionNotice, SessionSummary } from "./types";
import StatusSummary from "./StatusSummary";

type ConversationEntry = { id: string; role: "user" | "assistant"; body: string; turnID?: string; state?: "sending" | "sent" | "complete" | "unknown" };
type ActivityEntry = { id: string; label: string };
type PendingApproval = { approval: ApprovalRequest; supported: boolean; error?: string };

function errorText(error: unknown, outcomeMayBeUnknown = false): string {
  if (outcomeMayBeUnknown && error instanceof SessionUIError && error.status >= 500) return "The request outcome may be unknown. It was not retried; check this session before sending it again.";
  if (error instanceof Error) return error.message;
  return "The local session request could not be completed.";
}

function routeLabel(session: SessionSummary): string {
  return `${session.model} · ${session.effort}`;
}

export default function SessionApp() {
  const [status, setStatus] = useState<RoutingStatus>();
  const [sessions, setSessions] = useState<SessionSummary[]>([]);
  const [selectedID, setSelectedID] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [newSessionOpen, setNewSessionOpen] = useState(false);
  const [cwd, setCwd] = useState("");
  const [newPrompt, setNewPrompt] = useState("");
  const [turnText, setTurnText] = useState("");
  const [creating, setCreating] = useState(false);
  const [sending, setSending] = useState(false);
  const [activeTurns, setActiveTurns] = useState<Record<string, string>>({});
  const [entries, setEntries] = useState<Record<string, ConversationEntry[]>>({});
  const [activity, setActivity] = useState<Record<string, ActivityEntry[]>>({});
  const [approvals, setApprovals] = useState<Record<string, PendingApproval[]>>({});
  const [approvalBusy, setApprovalBusy] = useState<string>();
  const [connection, setConnection] = useState<SessionConnection>("connecting");
  const [error, setError] = useState<string>();

  const refreshSessions = useCallback(async () => {
    const [nextStatus, nextSessions] = await Promise.all([getRoutingStatus(), listSessions()]);
    setStatus(nextStatus);
    setSessions(nextSessions);
    setSelectedID((current) => current && nextSessions.some((item) => item.id === current) ? current : nextSessions[0]?.id);
    return nextSessions;
  }, []);

  useEffect(() => {
    let alive = true;
    Promise.all([getRoutingStatus(), listSessions()])
      .then(([nextStatus, nextSessions]) => {
        if (!alive) return;
        setStatus(nextStatus);
        setSessions(nextSessions);
        setSelectedID(nextSessions[0]?.id);
      })
      .catch((cause: unknown) => alive && setError(errorText(cause)))
      .finally(() => alive && setLoading(false));
    const timer = window.setInterval(() => {
      getRoutingStatus().then((nextStatus) => alive && setStatus(nextStatus)).catch(() => undefined);
    }, 30_000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, []);

  const selected = sessions.find((session) => session.id === selectedID);
  const activeTurn = selected ? activeTurns[selected.id] : undefined;
  const selectedEntries = selected ? entries[selected.id] ?? [] : [];
  const selectedActivity = selected ? activity[selected.id] ?? [] : [];
  const selectedApprovals = selected ? approvals[selected.id] ?? [] : [];

  useEffect(() => {
    if (!selectedID) return;
    setConnection("connecting");
    const subscription = subscribeToSession(selectedID, (notice) => {
      const id = selectedID;
      if (notice.type === "assistant_delta") {
        setEntries((current) => {
          const list = [...(current[id] ?? [])];
          const index = list.findIndex((entry) => entry.role === "assistant" && entry.id === notice.item_id);
          if (index >= 0) list[index] = { ...list[index], body: list[index].body + notice.delta, turnID: notice.turn_id };
          else list.push({ id: notice.item_id, role: "assistant", body: notice.delta, turnID: notice.turn_id });
          return { ...current, [id]: list.slice(-200) };
        });
      } else if (notice.type === "turn/started") {
        setActiveTurns((current) => ({ ...current, [id]: notice.turn_id }));
        setEntries((current) => {
          const list = (current[id] ?? []).map((entry) => entry.state === "sending" ? { ...entry, turnID: notice.turn_id, state: "sent" as const } : entry);
          return { ...current, [id]: list };
        });
        setActivity((current) => ({ ...current, [id]: [...(current[id] ?? []), { id: `start-${notice.turn_id}`, label: "Turn started" }].slice(-12) }));
      } else if (notice.type === "turn/completed") {
        setActiveTurns((current) => current[id] === notice.turn_id ? { ...current, [id]: "" } : current);
        setEntries((current) => ({ ...current, [id]: (current[id] ?? []).map((entry) => entry.turnID === notice.turn_id ? { ...entry, state: "complete" as const } : entry) }));
        setActivity((current) => ({ ...current, [id]: [...(current[id] ?? []), { id: `complete-${notice.turn_id}`, label: `Turn completed${notice.status ? ` · ${notice.status}` : ""}` }].slice(-12) }));
      } else if (notice.type === "approval") {
        setApprovals((current) => {
          const list = current[id] ?? [];
          const pending = { approval: notice.approval, supported: notice.supported, error: notice.error };
          return { ...current, [id]: [...list.filter((item) => item.approval.id !== notice.approval.id), pending].slice(-8) };
        });
      } else if (notice.type === "stream_error") {
        setError(notice.message);
      }
    }, undefined, setConnection);
    return () => subscription.close();
  }, [selectedID]);

  async function submitNewSession(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setCreating(true);
    setError(undefined);
    try {
      const result = await createSession(cwd, newPrompt);
      setSessions((current) => [result.session, ...current.filter((item) => item.id !== result.session.id)]);
      setSelectedID(result.session.id);
      setEntries((current) => ({ ...current, [result.session.id]: [{ id: result.turn_id, role: "user", body: newPrompt, turnID: result.turn_id, state: "sent" }] }));
      setActiveTurns((current) => ({ ...current, [result.session.id]: result.turn_id }));
      setCwd("");
      setNewPrompt("");
      setNewSessionOpen(false);
    } catch (cause: unknown) {
      setError(errorText(cause, true));
      try {
        const refreshed = await refreshSessions();
        if (refreshed.length) setSelectedID(refreshed[0].id);
      } catch {
        // The original safe error is more useful; no request is replayed.
      }
    } finally {
      setCreating(false);
    }
  }

  async function submitTurn(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selected || !turnText.trim() || activeTurns[selected.id]) return;
    const text = turnText;
    const localID = `local-${Date.now()}-${Math.random().toString(16).slice(2)}`;
    setSending(true);
    setError(undefined);
    setTurnText("");
    setActiveTurns((current) => ({ ...current, [selected.id]: "starting" }));
    setEntries((current) => ({ ...current, [selected.id]: [...(current[selected.id] ?? []), { id: localID, role: "user" as const, body: text, state: "sending" as const }].slice(-200) }));
    try {
      const result = await sendTurn(selected.id, text);
      setActiveTurns((current) => current[selected.id] === "starting" ? { ...current, [selected.id]: result.turn_id } : current);
      setEntries((current) => ({ ...current, [selected.id]: (current[selected.id] ?? []).map((entry) => entry.id === localID && entry.state === "sending" ? { ...entry, turnID: result.turn_id, state: "sent" } : entry) }));
    } catch (cause: unknown) {
      setActiveTurns((current) => current[selected.id] === "starting" ? { ...current, [selected.id]: "unknown" } : current);
      setEntries((current) => ({ ...current, [selected.id]: (current[selected.id] ?? []).map((entry) => entry.id === localID ? { ...entry, state: "unknown" } : entry) }));
      setError(errorText(cause, true));
    } finally {
      setSending(false);
    }
  }

  async function interruptActiveTurn() {
    if (!selected || !activeTurn || activeTurn === "starting" || activeTurn === "unknown") return;
    setError(undefined);
    try {
      await interruptTurn(selected.id, activeTurn);
    } catch (cause: unknown) {
      setError(errorText(cause));
    }
  }

  async function respondToApproval(approval: ApprovalRequest, decision: string) {
    if (!selected || !approval.decisions.includes(decision)) return;
    setApprovalBusy(approval.id);
    setError(undefined);
    try {
      await approveAction(selected.id, approval.id, decision);
      setApprovals((current) => ({ ...current, [selected.id]: (current[selected.id] ?? []).filter((item) => item.approval.id !== approval.id) }));
    } catch (cause: unknown) {
      setError(errorText(cause));
    } finally {
      setApprovalBusy(undefined);
    }
  }

  return (
    <div className="session-app">
      <header className="session-topbar">
        <a className="session-brand" href="/" aria-label="Codex task console home"><span className="brand-mark" aria-hidden="true" /><span>Codex sessions</span></a>
        <nav className="session-nav" aria-label="Primary navigation"><a href="/">Task console</a><span aria-current="page">Local sessions</span></nav>
        <span className="local-only"><span aria-hidden="true" />This browser only</span>
      </header>

      <main className="session-layout">
        <aside className="session-sidebar" aria-label="Session navigation">
          <div className="sidebar-heading"><div><p className="eyebrow">Interactive Codex</p><h1>Sessions</h1></div><button className="button button-primary" type="button" onClick={() => setNewSessionOpen((open) => !open)} aria-expanded={newSessionOpen}>New</button></div>
          {newSessionOpen && <form className="new-session-form" onSubmit={submitNewSession}>
            <label htmlFor="session-cwd">Working directory<input id="session-cwd" name="cwd" required value={cwd} onChange={(event) => setCwd(event.target.value)} placeholder="C:\\src\\project" autoComplete="off" /></label>
            <label htmlFor="session-first-prompt">First prompt<textarea id="session-first-prompt" name="prompt" required value={newPrompt} onChange={(event) => setNewPrompt(event.target.value)} placeholder="Describe what Codex should do…" rows={4} /></label>
            <button className="button button-primary button-wide" type="submit" disabled={creating}>{creating ? "Starting…" : "Start session"}</button>
            <p className="helper-copy">The service re-checks quota and pins the route. No route selector is exposed here.</p>
          </form>}

          <div className="session-list-heading"><h2>Recent</h2><span>{loading ? "Loading" : sessions.length}</span></div>
          {loading && <p className="sidebar-empty" role="status">Loading local sessions…</p>}
          {!loading && sessions.length === 0 && <p className="sidebar-empty">No local sessions yet. Start one to begin.</p>}
          <ul className="session-list" role="list">{sessions.map((session) => <li key={session.id}>
            <button type="button" className={session.id === selectedID ? "session-row is-selected" : "session-row"} aria-pressed={session.id === selectedID} onClick={() => setSelectedID(session.id)}>
              <span className="session-row-title">{session.id}</span><span className="session-row-route">{routeLabel(session)}</span><span className="session-row-mode">{session.mode === "normal" ? "OpenAI route" : session.mode === "quota_fallback" ? "Fallback route" : "Route unknown"}</span>
            </button>
          </li>)}</ul>
          <p className="sidebar-note">Each thread stays on its original provider and model.</p>
        </aside>

        <section className="session-workspace" aria-label="Codex conversation">
          {status && <StatusSummary status={status} />}
          {error && <div className="session-notice session-notice--error" role="alert">{error}<button type="button" className="notice-dismiss" aria-label="Dismiss error" onClick={() => setError(undefined)}>Dismiss</button></div>}
          {!selected && <div className="conversation-empty"><p className="eyebrow">Ready when you are</p><h2>Start a local session</h2><p>Choose a working directory and send a first prompt. This stays separate from the Proxmox task console.</p><button type="button" className="button button-primary" onClick={() => setNewSessionOpen(true)}>New session</button></div>}
          {selected && <>
            <header className="conversation-heading">
              <div><p className="eyebrow">Pinned route · {selected.mode === "normal" ? "OpenAI" : selected.mode === "quota_fallback" ? "Spark" : "Unknown"}</p><h2>{selected.model}</h2><p className="conversation-meta">{selected.effort} reasoning · session {selected.id}</p></div>
              <div className="conversation-actions"><span className={`connection-state connection-state--${connection}`} role="status">{connection === "connected" ? "Live" : connection === "reconnecting" ? "Reconnecting" : "Connecting"}</span><button type="button" className="button button-danger-quiet" disabled={!activeTurn || activeTurn === "starting" || activeTurn === "unknown"} onClick={interruptActiveTurn}>Interrupt</button></div>
            </header>

            <section className="conversation-stream" aria-label="Conversation events" aria-live="polite" aria-relevant="additions text">
              {selectedEntries.length === 0 && <div className="history-note"><h3>Thread context is preserved by Codex</h3><p>Earlier transcript text is not loaded into this browser view yet. New turns continue in the selected App Server thread; the UI never stores conversation text in browser storage.</p></div>}
              {selectedEntries.map((entry) => <article key={entry.id} className={`conversation-entry conversation-entry--${entry.role}`}>
                <div className="entry-heading"><h3>{entry.role === "user" ? "You" : "Codex"}</h3>{entry.state === "sending" && <span>Sending</span>}{entry.state === "unknown" && <span>Outcome unknown</span>}{entry.state === "complete" && <span>Complete</span>}</div>
                <p>{entry.body}</p>
              </article>)}
            </section>

            {selectedApprovals.length > 0 && <section className="approval-section" aria-labelledby="approval-heading"><div className="subsection-heading"><div><p className="eyebrow">Action required</p><h3 id="approval-heading">Codex is requesting approval</h3></div><span>{selectedApprovals.length}</span></div>
              {selectedApprovals.map((pending) => {
                const approval = pending.approval;
                return <article className="approval-request" key={approval.id}>
                {pending.supported ? <>
                  <p className="approval-reason">{approval.reason || (approval.method === "item/fileChange/requestApproval" ? "Allow this file change?" : "Allow this command to run?")}</p>
                  {approval.command && <pre>{approval.command}</pre>}
                  {approval.cwd && <p className="approval-cwd">Working directory: <code>{approval.cwd}</code></p>}
                  <div className="approval-actions">{approval.decisions.map((decision) => <button key={decision} type="button" className={decision === "accept" ? "button button-quiet" : "button button-quiet"} disabled={approvalBusy === approval.id} onClick={() => respondToApproval(approval, decision)}>{approvalBusy === approval.id ? "Responding…" : decision === "accept" ? "Allow once" : decision === "decline" ? "Decline" : "Cancel"}</button>)}</div>
                </> : <p className="approval-reason">This request needs a permission change that this interface does not support. It was rejected by the App Server client.</p>}
              </article>;
              })}
            </section>}

            <section className="activity-section" aria-labelledby="activity-heading"><div className="subsection-heading"><div><p className="eyebrow">Bounded App Server events</p><h3 id="activity-heading">Activity</h3></div><span>{selectedActivity.length}</span></div>
              {selectedActivity.length === 0 ? <p className="activity-empty">Turn starts, completions, and approval requests appear here. Raw tool output and hidden reasoning are not mirrored.</p> : <ol className="activity-list">{selectedActivity.map((item) => <li key={item.id}>{item.label}</li>)}</ol>}
            </section>

            <form className="turn-composer" onSubmit={submitTurn}>
              <label htmlFor="turn-text">Message Codex<textarea id="turn-text" name="text" value={turnText} onChange={(event) => setTurnText(event.target.value)} placeholder="Continue this thread…" rows={3} disabled={Boolean(activeTurn) || sending} /></label>
              <div className="composer-footer"><span>{activeTurn === "unknown" ? "Turn outcome unknown; inspect the thread before continuing." : activeTurn ? "A turn is active. Interrupt it or wait for completion." : "Turns are never automatically retried after an error."}</span><button className="button button-secondary" type="submit" disabled={!turnText.trim() || Boolean(activeTurn) || sending}>{sending ? "Sending…" : "Send"}</button></div>
            </form>
          </>}
        </section>
      </main>
    </div>
  );
}
