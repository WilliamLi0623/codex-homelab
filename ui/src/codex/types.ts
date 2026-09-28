export type RouteMode = "normal" | "quota_fallback" | "unknown";

export interface RoutingStatus {
  mode: RouteMode;
  observed_mode: RouteMode;
  observed_at: string | null;
  generation: number;
  fresh: boolean;
  published: boolean;
  can_start: boolean;
  fallback_enabled: boolean;
}

export interface SessionSummary {
  id: string;
  provider: string;
  model: string;
  effort: string;
  mode: string;
  generation: number;
  created_at: string;
}

export type SessionNotice =
  | { type: "assistant_delta"; thread_id: string; turn_id: string; item_id: string; delta: string }
  | { type: "turn/started" | "turn/completed"; thread_id: string; turn_id: string; status: string }
  | { type: "stream_error"; message: string }
  | { type: "approval"; thread_id: string; turn_id: string; approval: ApprovalRequest; supported: boolean; error?: string };

export interface ApprovalRequest {
  id: string;
  method: string;
  threadId: string;
  turnId: string;
  itemId: string;
  kind?: string;
  command?: string;
  cwd?: string;
  reason?: string;
  decisions: string[];
}
