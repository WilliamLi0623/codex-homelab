import { FormEvent, useEffect, useMemo, useState } from "react";
import { ControllerError, createTask, listTasks } from "./api/controller";
import type { Task } from "./types";

const initialForm = { repository: "", base_ref: "main", objective: "", profile: "openai-primary" };

function stateLabel(state: Task["state"]): string {
  return state.replaceAll("_", " ").toLowerCase();
}

function makeIdempotencyKey(): string {
  return `ui-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export default function App() {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [selectedID, setSelectedID] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<ControllerError>();
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [form, setForm] = useState(initialForm);
  const [creating, setCreating] = useState(false);
  const [createdMessage, setCreatedMessage] = useState<string>();

  useEffect(() => {
    let active = true;
    listTasks()
      .then((next) => {
        if (active) {
          setTasks(next);
          setSelectedID(next[0]?.id);
        }
      })
      .catch((cause: unknown) => {
        if (active) setError(cause instanceof ControllerError ? cause : new ControllerError(0, "Could not load tasks."));
      })
      .finally(() => active && setLoading(false));
    return () => {
      active = false;
    };
  }, []);

  const selected = useMemo(() => tasks.find((task) => task.id === selectedID), [selectedID, tasks]);

  async function submitTask(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setCreating(true);
    setError(undefined);
    setCreatedMessage(undefined);
    try {
      const task = await createTask({ ...form, idempotency_key: makeIdempotencyKey() });
      setTasks((current) => [task, ...current.filter((item) => item.id !== task.id)]);
      setSelectedID(task.id);
      setForm(initialForm);
      setCreatedMessage("Task submitted. Start an attempt from the task detail view.");
    } catch (cause: unknown) {
      setError(cause instanceof ControllerError ? cause : new ControllerError(0, "Could not submit task."));
    } finally {
      setCreating(false);
    }
  }

  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="brand"><span className="brand-mark" aria-hidden="true" /> <span>Codex task console</span></div>
        <button type="button" className="menu-button" aria-expanded={mobileNavOpen} aria-controls="main-nav" onClick={() => setMobileNavOpen((open) => !open)}>Menu</button>
        <nav id="main-nav" className={mobileNavOpen ? "main-nav is-open" : "main-nav"} aria-label="Primary navigation">
          <a href="#tasks" onClick={() => setMobileNavOpen(false)}>Tasks</a>
          <a href="#new-task" onClick={() => setMobileNavOpen(false)}>New task</a>
          <a href="#operations" onClick={() => setMobileNavOpen(false)}>Operations</a>
        </nav>
        <span className="connection-status" aria-label="Controller connection status">Private console</span>
      </header>

      <main className="main-content">
        <section className="page-intro" aria-labelledby="page-title">
          <div>
            <p className="eyebrow">Controller / operator view</p>
            <h1 id="page-title">Tasks</h1>
            <p className="intro-copy">Create work, follow durable state, and continue an existing attempt without exposing infrastructure credentials.</p>
          </div>
          <a className="button button-primary" href="#new-task">New task</a>
        </section>

        {error && <div className="notice notice-error" role="alert"><strong>Could not complete that request.</strong><span>{error.message}</span></div>}
        {createdMessage && <div className="notice notice-success" role="status">{createdMessage}</div>}

        <section className="metric-row" aria-label="Task summary">
          <div><span className="metric-label">Visible tasks</span><strong className="metric-value">{tasks.length}</strong></div>
          <div><span className="metric-label">Active</span><strong className="metric-value">{tasks.filter((task) => ["RUNNING", "DISPATCHED", "VALIDATING", "PUBLISHING"].includes(task.state)).length}</strong></div>
          <div><span className="metric-label">Needs attention</span><strong className="metric-value">{tasks.filter((task) => ["FAILED", "BLOCKED"].includes(task.state)).length}</strong></div>
        </section>

        <section id="tasks" className="workspace-grid">
          <div className="task-list-panel" aria-labelledby="task-list-title">
            <div className="section-heading"><div><p className="eyebrow">Durable records</p><h2 id="task-list-title">Recent tasks</h2></div><span className="muted-label">{loading ? "Loading" : `${tasks.length} total`}</span></div>
            {loading && <p className="state-message" role="status">Loading tasks…</p>}
            {!loading && tasks.length === 0 && <p className="state-message">No tasks yet. Create one to begin.</p>}
            {!loading && tasks.length > 0 && <ul className="task-list" role="list">{tasks.map((task) => <li key={task.id}><button type="button" className={task.id === selectedID ? "task-row is-selected" : "task-row"} onClick={() => setSelectedID(task.id)}><span className="task-row-main"><strong>{task.objective}</strong><span>{task.repository} · {task.base_ref}</span></span><span className={`state state-${task.state.toLowerCase()}`}>{stateLabel(task.state)}</span></button></li>)}</ul>}
          </div>

          <aside className="task-detail-panel" aria-labelledby="detail-title">
            <div className="section-heading"><div><p className="eyebrow">Selected task</p><h2 id="detail-title">{selected ? selected.objective : "No task selected"}</h2></div></div>
            {selected ? <dl className="detail-list"><div><dt>State</dt><dd><span className={`state state-${selected.state.toLowerCase()}`}>{stateLabel(selected.state)}</span></dd></div><div><dt>Repository</dt><dd>{selected.repository}</dd></div><div><dt>Base ref</dt><dd>{selected.base_ref}</dd></div><div><dt>Execution</dt><dd>{selected.execution_class}</dd></div><div><dt>Task ID</dt><dd className="mono">{selected.id}</dd></div></dl> : <p className="state-message">Select a task to inspect its event timeline and continuation controls.</p>}
            <p className="panel-footnote">Event timeline and guarded operations arrive in the next UI slice.</p>
          </aside>
        </section>

        <section id="new-task" className="new-task-section" aria-labelledby="new-task-title">
          <div className="section-heading"><div><p className="eyebrow">Create work</p><h2 id="new-task-title">New task</h2></div></div>
          <form className="task-form" onSubmit={submitTask}>
            <label htmlFor="repository">Repository<input id="repository" name="repository" required value={form.repository} onChange={(event) => setForm({ ...form, repository: event.target.value })} placeholder="owner/repository" /></label>
            <label htmlFor="base-ref">Base ref<input id="base-ref" name="base_ref" required value={form.base_ref} onChange={(event) => setForm({ ...form, base_ref: event.target.value })} /></label>
            <label htmlFor="profile">Profile<select id="profile" name="profile" value={form.profile} onChange={(event) => setForm({ ...form, profile: event.target.value })}><option value="openai-primary">OpenAI primary</option><option value="muse-spark-1.3-contributor">Muse Spark</option><option value="glm-5.3-flash">GLM-5.3 Flash</option></select></label>
            <label className="field-wide" htmlFor="objective">Objective<textarea id="objective" name="objective" required rows={4} value={form.objective} onChange={(event) => setForm({ ...form, objective: event.target.value })} placeholder="Describe the change and the evidence you expect." /></label>
            <div className="form-actions"><button type="submit" className="button button-primary" disabled={creating}>{creating ? "Submitting…" : "Submit task"}</button><span className="helper-text">The Controller owns scheduling, worker allocation, and provider routing.</span></div>
          </form>
        </section>
      </main>
    </div>
  );
}
