import { describe, expect, it } from "vitest";
import { mergeHistoryEntries, type ConversationEntry } from "./history";

describe("conversation history hydration", () => {
  it("keeps server chronology and preserves a new repeated user prompt", () => {
    const live: ConversationEntry[] = [
      { id: "item-2", role: "assistant", body: "partial answer" },
      { id: "local-user", role: "user", body: "next question", state: "sent" },
    ];

    expect(mergeHistoryEntries([
      { id: "item-1", role: "user", text: "first question" },
      { id: "item-2", role: "assistant", text: "complete answer" },
      { id: "item-3", role: "user", text: "next question" },
    ], live)).toEqual([
      { id: "item-1", role: "user", body: "first question" },
      { id: "item-2", role: "assistant", body: "partial answer" },
      { id: "item-3", role: "user", body: "next question" },
      { id: "local-user", role: "user", body: "next question", state: "sent" },
    ]);
  });

  it("does not collapse distinct assistant messages just because their text matches", () => {
    expect(mergeHistoryEntries(
      [{ id: "assistant-old", role: "assistant", text: "same answer" }],
      [{ id: "assistant-new", role: "assistant", body: "same answer" }],
    )).toEqual([
      { id: "assistant-old", role: "assistant", body: "same answer" },
      { id: "assistant-new", role: "assistant", body: "same answer" },
    ]);
  });
});
