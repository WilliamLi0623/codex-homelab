import { describe, expect, it } from "vitest";
import { formatHistoricalAttemptProfile } from "./TaskDetail";

describe("historical attempt profile", () => {
  it("keeps the persisted profile visible as read-only metadata", () => {
    expect(formatHistoricalAttemptProfile("openai-primary")).toBe("openai-primary");
    expect(formatHistoricalAttemptProfile("muse-spark-1.3-contributor")).toBe("muse-spark-1.3-contributor");
  });
});
