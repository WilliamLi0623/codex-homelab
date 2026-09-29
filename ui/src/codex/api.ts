import type { ApprovalRequest, RoutingStatus, SessionHistory, SessionNotice, SessionSummary } from "./types";

export class SessionUIError extends Error {
  constructor(readonly status: number) {
    super(safeMessage(status));
    this.name = "SessionUIError";
  }
}

export interface CreatedSession {
  session: SessionSummary;
  turn_id: string;
}

export interface EventSourceLike {
  addEventListener(type: string, listener: (event: MessageEvent<string>) => void): void;
  close(): void;
  onerror: ((event: Event) => void) | null;
  onopen: ((event: Event) => void) | null;
}

export type SessionConnection = "connecting" | "connected" | "reconnecting";

function safeMessage(status: number): string {
  if (status === 401 || status === 403) return "The local session UI authorization expired. Reload this page.";
  if (status === 404) return "This local session is no longer available.";
  if (status === 409) return "That approval is no longer pending or is not supported.";
  if (status >= 500) return "The local Codex session service is unavailable.";
  return "The request could not be completed.";
}

async function requestJSON<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(path, {
      credentials: "same-origin",
      ...init,
      headers: {
        Accept: "application/json",
        ...(init?.body ? { "Content-Type": "application/json" } : {}),
        ...init?.headers,
      },
    });
  } catch {
    throw new SessionUIError(503);
  }
  if (!response.ok) throw new SessionUIError(response.status);
  if (response.status === 204) return undefined as T;
  try {
    return await response.json() as T;
  } catch {
    throw new SessionUIError(502);
  }
}

export async function getRoutingStatus(): Promise<RoutingStatus> {
  return requestJSON<RoutingStatus>("/api/status");
}

export async function listSessions(): Promise<SessionSummary[]> {
  const result = await requestJSON<{ sessions: SessionSummary[] }>("/api/sessions");
  return result.sessions;
}

export function getSessionHistory(threadID: string): Promise<SessionHistory> {
  return requestJSON<SessionHistory>(`/api/sessions/${encodeURIComponent(threadID)}/history`);
}

export function createSession(cwd: string, prompt: string): Promise<CreatedSession> {
  return requestJSON<CreatedSession>("/api/sessions", {
    method: "POST",
    body: JSON.stringify({ cwd, prompt }),
  });
}

export function sendTurn(threadID: string, text: string): Promise<{ turn_id: string }> {
  return requestJSON<{ turn_id: string }>(`/api/sessions/${encodeURIComponent(threadID)}/turns`, {
    method: "POST",
    body: JSON.stringify({ text }),
  });
}

export function interruptTurn(threadID: string, turnID: string): Promise<void> {
  return requestJSON<void>(`/api/sessions/${encodeURIComponent(threadID)}/interrupt`, {
    method: "POST",
    body: JSON.stringify({ turn_id: turnID }),
  });
}

export function approveAction(threadID: string, id: string, decision: string): Promise<void> {
  if (!["accept", "decline", "cancel"].includes(decision)) {
    return Promise.reject(new SessionUIError(400));
  }
  return requestJSON<void>("/api/approvals", {
    method: "POST",
    body: JSON.stringify({ thread_id: threadID, id, decision }),
  });
}

const maxEventBytes = 48 * 1024;
const maxDeltaBytes = 32 * 1024;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function safeApproval(value: unknown): ApprovalRequest | undefined {
  if (!isRecord(value) || typeof value.id !== "string" || typeof value.method !== "string" || typeof value.threadId !== "string" || typeof value.turnId !== "string" || typeof value.itemId !== "string" || !Array.isArray(value.decisions)) return undefined;
  const decisions = value.decisions.filter((item): item is string => typeof item === "string" && ["accept", "decline", "cancel"].includes(item));
  if (!decisions.length) return undefined;
  return {
    id: value.id,
    method: value.method,
    threadId: value.threadId,
    turnId: value.turnId,
    itemId: value.itemId,
    ...(typeof value.kind === "string" ? { kind: value.kind.slice(0, 256) } : {}),
    ...(typeof value.command === "string" ? { command: value.command.slice(0, 16 * 1024) } : {}),
    ...(typeof value.cwd === "string" ? { cwd: value.cwd.slice(0, 4 * 1024) } : {}),
    ...(typeof value.reason === "string" ? { reason: value.reason.slice(0, 4 * 1024) } : {}),
    decisions,
  };
}

function parseNotice(data: string, threadID: string): SessionNotice | undefined {
  if (new TextEncoder().encode(data).byteLength > maxEventBytes) return undefined;
  let value: unknown;
  try {
    value = JSON.parse(data) as unknown;
  } catch {
    return undefined;
  }
  if (!isRecord(value) || typeof value.type !== "string") return undefined;
  if (value.type === "assistant_delta" && value.thread_id === threadID && typeof value.turn_id === "string" && typeof value.item_id === "string" && typeof value.delta === "string" && new TextEncoder().encode(value.delta).byteLength <= maxDeltaBytes) {
    return { type: "assistant_delta", thread_id: threadID, turn_id: value.turn_id, item_id: value.item_id, delta: value.delta };
  }
  if ((value.type === "turn/started" || value.type === "turn/completed") && value.thread_id === threadID && typeof value.turn_id === "string") {
    return { type: value.type, thread_id: threadID, turn_id: value.turn_id, status: typeof value.status === "string" ? value.status.slice(0, 128) : "" };
  }
  if (value.type === "approval" && value.thread_id === threadID) {
    const approval = safeApproval(value.approval);
    if (!approval || approval.threadId !== threadID) return undefined;
    return { type: "approval", thread_id: threadID, turn_id: typeof value.turn_id === "string" ? value.turn_id : approval.turnId, approval, supported: value.supported === true, ...(typeof value.error === "string" ? { error: value.error.slice(0, 512) } : {}) };
  }
  if (value.type === "stream_error" && typeof value.message === "string") {
    return { type: "stream_error", message: value.message.slice(0, 512) };
  }
  return undefined;
}

export function subscribeToSession(
  threadID: string,
  onNotice: (notice: SessionNotice) => void,
  factory: (url: string) => EventSourceLike = (url) => new EventSource(url),
  onConnection?: (state: SessionConnection) => void,
): { close(): void } {
  onConnection?.("connecting");
  const source = factory(`/api/sessions/${encodeURIComponent(threadID)}/events`);
  source.onopen = () => onConnection?.("connected");
  source.onerror = () => onConnection?.("reconnecting");
  source.addEventListener("message", (event) => {
    if (event.data.length > maxEventBytes) return;
    const notice = parseNotice(event.data, threadID);
    if (notice) onNotice(notice);
  });
  return { close: () => source.close() };
}
