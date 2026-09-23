import { FormEvent, useEffect, useMemo, useState } from "react";
import { cancelTask, continueTask, ControllerError, getTask, getTaskAttempts, getTaskEvents, getTaskMessages, retryTask, startTaskAttempt } from "../api/controller";
import { subscribeToTaskEvents } from "../api/events";
import type { Task, TaskAttempt, TaskEvent, TaskMessage } from "../types";

interface TaskDetailProps {
  taskID: string;
  onTaskChanged(task: Task): void;
}

const terminalTaskStates = new Set(["SUCCEEDED", "FAILED", "BLOCKED", "CANCELLED"]);
const terminalAttemptStates = new Set(["COMPLETED", "PROVIDER_FAILED", "EXECUTION_FAILED", "VALIDATION_FAILED", "CANCELLED"]);

export function formatHistoricalAttemptProfile(profile: string): string {
  return profile;
}

function stateLabel(state: string): string {
  return state.replaceAll("_", " ").toLowerCase();
}

function makeIdempotencyKey(): string {
  return `ui-turn-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export default function TaskDetail({ taskID, onTaskChanged }: TaskDetailProps) {
  const [task, setTask] = useState<Task>();
  const [attempts, setAttempts] = useState<TaskAttempt[]>([]);
  const [messages, setMessages] = useState<TaskMessage[]>([]);
  const [events, setEvents] = useState<TaskEvent[]>([]);
  const [body, setBody] = useState("");
  const [loading, setLoading] = useState(true);
  const [working, setWorking] = useState(false);
  const [error, setError] = useState<ControllerError>();
  const [notice, setNotice] = useState<string>();

  const latestAttempt = useMemo(() => attempts[attempts.length - 1], [attempts]);
  const canContinue = Boolean(latestAttempt && !terminalAttemptStates.has(latestAttempt.state) && body.trim());

  async function refresh() {
    const [nextTask, nextAttempts, nextMessages, nextEvents] = await Promise.all([
      getTask(taskID),
      getTaskAttempts(taskID),
      getTaskMessages(taskID),
      getTaskEvents(taskID),
    ]);
    setTask(nextTask);
    setAttempts(nextAttempts);
    setMessages(nextMessages);
    setEvents(nextEvents);
    onTaskChanged(nextTask);
  }

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError(undefined);
    setNotice(undefined);
    void refresh()
      .catch((cause: unknown) => {
        if (active) setError(cause instanceof ControllerError ? cause : new ControllerError(0, "Could not load task details."));
      })
      .finally(() => active && setLoading(false));
    const subscription = subscribeToTaskEvents(taskID, {
      onEvent(event) {
        if (!active) return;
        setEvents((current) => current.some((item) => item.id === event.id) ? current : [...current, event]);
        void refresh().catch(() => undefined);
      },
      onError(cause) {
        if (active) setError(new ControllerError(0, cause.message));
      },
    });
    return () => {
      active = false;
      subscription.close();
    };
  }, [taskID]);

  async function runOperation(operation: () => Promise<Task>, success: string) {
    setWorking(true);
    setError(undefined);
    setNotice(undefined);
    try {
      const nextTask = await operation();
      setTask(nextTask);
      onTaskChanged(nextTask);
      await refresh();
      setNotice(success);
    } catch (cause: unknown) {
      setError(cause instanceof ControllerError ? cause : new ControllerError(0, "The operation could not be completed."));
    } finally {
      setWorking(false);
    }
  }

  async function submitAttempt(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setWorking(true);
    setError(undefined);
    try {
      const result = await startTaskAttempt(taskID);
      setTask(result.task);
      setAttempts((current) => [...current, result.attempt]);
      onTaskChanged(result.task);
      setNotice(`Attempt ${result.attempt.number} created.`);
    } catch (cause: unknown) {
      setError(cause instanceof ControllerError ? cause : new ControllerError(0, "The attempt could not be started."));
    } finally {
      setWorking(false);
    }
  }

  async function submitContinuation(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!latestAttempt || !body.trim()) return;
    setWorking(true);
    setError(undefined);
    setNotice(undefined);
    const message = body.trim();
    try {
      const result = await continueTask(taskID, latestAttempt.id, message, makeIdempotencyKey());
      setBody("");
      setNotice(`Continuation ${stateLabel(result.state)}.`);
      await refresh();
    } catch (cause: unknown) {
      setError(cause instanceof ControllerError ? cause : new ControllerError(0, "The continuation could not be delivered."));
    } finally {
      setWorking(false);
    }
  }

  if (loading) return <aside className="task-detail-panel"><p className="state-message">Loading task detail…</p></aside>;
  if (!task) return <aside className="task-detail-panel"><p className="state-message">Task detail is unavailable.</p></aside>;

  return (
    <aside className="task-detail-panel" aria-labelledby="detail-title">
      <div className="section-heading"><div><p className="eyebrow">Selected task</p><h2 id="detail-title">{task.objective}</h2></div><span className={`state state-${task.state.toLowerCase()}`}>{stateLabel(task.state)}</span></div>
      {error && <div className="notice notice-error" role="alert">{error.message}</div>}
      {notice && <div className="notice notice-success" role="status">{notice}</div>}
      <dl className="detail-list"><div><dt>Repository</dt><dd>{task.repository}</dd></div><div><dt>Base ref</dt><dd>{task.base_ref}</dd></div><div><dt>Execution</dt><dd>{task.execution_class}</dd></div><div><dt>Task ID</dt><dd className="mono">{task.id}</dd></div></dl>

      <section className="detail-section" aria-labelledby="attempts-title"><div className="subsection-heading"><h3 id="attempts-title">Attempts</h3><span className="muted-label">{attempts.length}</span></div>
        {attempts.length === 0 && <form className="inline-form" onSubmit={submitAttempt}><label htmlFor="attempt-profile">Worker role<output id="attempt-profile" aria-readonly="true">Automatic (Codex quota state)</output></label><button className="button button-primary" disabled={working}>Start attempt</button></form>}
        {attempts.length > 0 && <ol className="attempt-list">{attempts.map((attempt) => <li key={attempt.id}><span><strong>Attempt {attempt.number}</strong><small className="mono">Historical profile: {formatHistoricalAttemptProfile(attempt.model_profile)}</small></span><span className={`state state-${attempt.state.toLowerCase()}`}>{stateLabel(attempt.state)}</span></li>)}</ol>}
      </section>

      <section className="detail-section" aria-labelledby="messages-title"><div className="subsection-heading"><h3 id="messages-title">Conversation</h3><span className="muted-label">{messages.length} messages</span></div>
        <div className="message-list">{messages.length === 0 ? <p className="state-message">No operator messages yet.</p> : messages.map((message) => <article className={`message message-${message.role}`} key={message.id}><div className="message-meta">{message.role} · {new Date(message.created_at).toLocaleString()}</div><p>{message.body}</p></article>)}</div>
        {latestAttempt && !terminalAttemptStates.has(latestAttempt.state) && <form className="continuation-form" onSubmit={submitContinuation}><label htmlFor="continuation-body">Continue this attempt<textarea id="continuation-body" rows={3} value={body} onChange={(event) => setBody(event.target.value)} placeholder="Send a bounded instruction to the running attempt." /></label><button className="button button-primary" disabled={!canContinue || working}>{working ? "Sending…" : "Send continuation"}</button></form>}
      </section>

      <section className="detail-section" aria-labelledby="events-title"><div className="subsection-heading"><h3 id="events-title">Event timeline</h3><span className="muted-label">live cursor</span></div><ol className="event-list">{events.length === 0 ? <li className="state-message">No events recorded.</li> : events.slice().reverse().map((event) => <li key={event.id}><span className="event-dot" aria-hidden="true" /><span><strong>{event.type}</strong><small>{new Date(event.created_at).toLocaleString()}</small></span></li>)}</ol></section>

      <div className="guarded-actions"><button className="button button-quiet" disabled={working || terminalTaskStates.has(task.state)} onClick={() => void runOperation(() => cancelTask(taskID), "Task cancellation requested.")}>Cancel</button><button className="button button-quiet" disabled={working || !["FAILED", "BLOCKED", "CANCELLED"].includes(task.state)} onClick={() => void runOperation(() => retryTask(taskID).then((result) => result.task), "Retry attempt created.")}>Retry</button></div>
    </aside>
  );
}
