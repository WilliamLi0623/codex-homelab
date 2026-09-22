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

export interface CreateTaskInput {
  repository: string;
  base_ref: string;
  objective: string;
  profile?: string;
  idempotency_key: string;
}
