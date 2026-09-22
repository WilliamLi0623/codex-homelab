import type { TaskEvent } from "../types";

type EventPayload = { data: string; lastEventId?: string };
type EventListener = (event: EventPayload) => void;

export interface EventSourceLike {
  onerror: (() => void) | null;
  close(): void;
  addEventListener(type: string, listener: EventListener): void;
}

export interface TaskEventSubscription {
  close(): void;
}

export interface TaskEventHandlers {
  onEvent(event: TaskEvent): void;
  onTerminal?(event: TaskEvent): void;
  onError?(error: Error): void;
}

export type EventSourceFactory = (url: string) => EventSourceLike;

const terminalEvents = new Set(["task.succeeded", "task.failed", "task.blocked", "task.cancelled", "attempt.COMPLETED", "attempt.PROVIDER_FAILED", "attempt.EXECUTION_FAILED", "attempt.VALIDATION_FAILED", "attempt.CANCELLED"]);
const knownEventNames = ["task.created", "task.message_received", "task.continuation_requested", "task.succeeded", "task.failed", "task.blocked", "task.cancelled", "attempt.CREATED", "attempt.COMPLETED", "attempt.PROVIDER_FAILED", "attempt.EXECUTION_FAILED", "attempt.VALIDATION_FAILED", "attempt.CANCELLED"];

export function subscribeToTaskEvents(taskID: string, handlers: TaskEventHandlers, factory: EventSourceFactory = (url) => new EventSource(url) as unknown as EventSourceLike): TaskEventSubscription {
  let cursor = "";
  let closed = false;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  let reconnectDelay = 250;
  let source: EventSourceLike | undefined;

  function handle(raw: EventPayload) {
    if (closed || !raw.data) return;
    let event: TaskEvent;
    try {
      event = JSON.parse(raw.data) as TaskEvent;
    } catch {
      handlers.onError?.(new Error("The event stream returned malformed JSON."));
      return;
    }
    const nextID = event.id || raw.lastEventId || "";
    if (nextID && nextID === cursor) return;
    if (nextID) cursor = nextID;
    handlers.onEvent(event);
    if (terminalEvents.has(event.type)) {
      handlers.onTerminal?.(event);
      close();
    }
  }

  function connect() {
    if (closed) return;
    const suffix = cursor ? `?after=${encodeURIComponent(cursor)}` : "";
    source = factory(`/ui/api/tasks/${encodeURIComponent(taskID)}/events/stream${suffix}`);
    source.onerror = () => {
      if (closed) return;
      source?.close();
      reconnectTimer = setTimeout(() => {
        reconnectTimer = undefined;
        reconnectDelay = Math.min(reconnectDelay * 2, 5000);
        connect();
      }, reconnectDelay);
    };
    source.addEventListener("message", handle);
    for (const eventName of knownEventNames) source.addEventListener(eventName, handle);
  }

  function close() {
    closed = true;
    if (reconnectTimer !== undefined) clearTimeout(reconnectTimer);
    source?.close();
  }

  connect();
  return { close };
}
