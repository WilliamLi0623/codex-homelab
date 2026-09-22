import type { ContinuationResult, CreateTaskInput, Task, TaskAttempt, TaskEvent, TaskMessage } from "../types";

const API_ROOT = "/ui/api";

export class ControllerError extends Error {
  readonly status: number;
  readonly errorClass: "auth" | "not-found" | "conflict" | "unavailable" | "request";

  constructor(status: number, message: string) {
    super(message);
    this.name = "ControllerError";
    this.status = status;
    this.errorClass = classifyStatus(status);
  }
}

function classifyStatus(status: number): ControllerError["errorClass"] {
  if (status === 401 || status === 403) return "auth";
  if (status === 404) return "not-found";
  if (status === 409) return "conflict";
  if (status >= 500) return "unavailable";
  return "request";
}

function safeMessage(status: number): string {
  if (status === 401 || status === 403) return "This console is not authorized.";
  if (status === 404) return "The requested task was not found.";
  if (status === 409) return "The operation conflicts with the current task state.";
  if (status >= 500) return "The Controller is temporarily unavailable.";
  return "The request could not be completed.";
}

async function requestJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${API_ROOT}${path}`, {
    credentials: "same-origin",
    ...init,
    headers: {
      Accept: "application/json",
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...init?.headers,
    },
  });
  if (!response.ok) {
    throw new ControllerError(response.status, safeMessage(response.status));
  }
  return (await response.json()) as T;
}

export async function listTasks(): Promise<Task[]> {
  const response = await requestJSON<{ tasks: Task[] }>("/tasks");
  return response.tasks;
}

export async function getTask(taskID: string): Promise<Task> {
  const response = await requestJSON<{ task: Task }>(`/tasks/${encodeURIComponent(taskID)}`);
  return response.task;
}

export async function getTaskAttempts(taskID: string): Promise<TaskAttempt[]> {
  const response = await requestJSON<{ attempts: TaskAttempt[] }>(`/tasks/${encodeURIComponent(taskID)}/attempts`);
  return response.attempts;
}

export async function getTaskMessages(taskID: string): Promise<TaskMessage[]> {
  const response = await requestJSON<{ messages: TaskMessage[] }>(`/tasks/${encodeURIComponent(taskID)}/messages`);
  return response.messages;
}

export async function getTaskEvents(taskID: string, after?: string): Promise<TaskEvent[]> {
  const query = after ? `?after=${encodeURIComponent(after)}` : "";
  const response = await requestJSON<{ events: TaskEvent[] }>(`/tasks/${encodeURIComponent(taskID)}/events${query}`);
  return response.events;
}

export async function startTaskAttempt(taskID: string, profile: string): Promise<{ task: Task; attempt: TaskAttempt }> {
  return requestJSON<{ task: Task; attempt: TaskAttempt }>(`/tasks/${encodeURIComponent(taskID)}/attempts`, {
    method: "POST",
    body: JSON.stringify({ profile }),
  });
}

export async function continueTask(taskID: string, attemptID: string, body: string, idempotencyKey: string): Promise<ContinuationResult> {
  return requestJSON<ContinuationResult>(`/tasks/${encodeURIComponent(taskID)}/turns`, {
    method: "POST",
    body: JSON.stringify({ attempt_id: attemptID, body, idempotency_key: idempotencyKey }),
  });
}

export async function cancelTask(taskID: string): Promise<Task> {
  const response = await requestJSON<{ task: Task }>(`/tasks/${encodeURIComponent(taskID)}/cancel`, { method: "POST" });
  return response.task;
}

export async function retryTask(taskID: string): Promise<{ task: Task; attempt: TaskAttempt }> {
  return requestJSON<{ task: Task; attempt: TaskAttempt }>(`/tasks/${encodeURIComponent(taskID)}/retry`, { method: "POST" });
}

export async function createTask(input: CreateTaskInput): Promise<Task> {
  const response = await requestJSON<{ task: Task }>("/tasks", {
    method: "POST",
    body: JSON.stringify(input),
  });
  return response.task;
}
