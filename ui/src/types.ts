export type TaskState =
  | "RECEIVED"
  | "PLANNED"
  | "WAITING_FOR_CAPACITY"
  | "QUEUED"
  | "DISPATCHED"
  | "RUNNING"
  | "VALIDATING"
  | "PUBLISHING"
  | "SUCCEEDED"
  | "FAILED"
  | "BLOCKED"
  | "CANCELLED";

export interface Task {
  id: string;
  repository: string;
  base_ref: string;
  objective: string;
  execution_class: string;
  state: TaskState;
}

export interface TaskEvent {
  id: string;
  type: string;
  created_at: string;
}

export interface TaskAttempt {
  id: string;
  number: number;
  model_profile: string;
  state: string;
}

export type WorkerModelProfile = "worker";

export interface TaskMessage {
  id: string;
  role: string;
  body: string;
  created_at: string;
}

export interface ContinuationResult {
  id: string;
  task_id: string;
  attempt_id: string;
  idempotency_key: string;
  state: string;
  error_summary?: string;
}

export interface CreateTaskInput {
  repository: string;
  base_ref: string;
  objective: string;
  idempotency_key: string;
}
