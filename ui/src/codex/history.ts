import type { HistoryMessage } from "./types";

export type ConversationEntry = {
  id: string;
  role: "user" | "assistant";
  body: string;
  turnID?: string;
  state?: "sending" | "sent" | "complete" | "unknown";
};

export function mergeHistoryEntries(history: HistoryMessage[], live: ConversationEntry[]): ConversationEntry[] {
  const merged = history.map<ConversationEntry>(({ id, role, text }) => ({ id, role, body: text }));
  for (const entry of live) {
    const sameID = merged.findIndex((candidate) => candidate.id === entry.id);
    if (sameID >= 0) {
      merged[sameID] = entry;
      continue;
    }
    merged.push(entry);
  }
  return merged.slice(-200);
}
