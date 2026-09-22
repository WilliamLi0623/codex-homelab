import { afterEach, describe, expect, it, vi } from "vitest";
import { subscribeToTaskEvents, type EventSourceLike } from "./events";

class FakeSource implements EventSourceLike {
  readonly url: string;
  readonly listeners = new Map<string, ((event: { data: string; lastEventId?: string }) => void)[]>();
  onerror: (() => void) | null = null;
  closed = false;

  constructor(url: string) {
    this.url = url;
  }

  addEventListener(type: string, listener: (event: { data: string; lastEventId?: string }) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }

  close() {
    this.closed = true;
  }

  emit(type: string, data: string, lastEventId?: string) {
    for (const listener of this.listeners.get(type) ?? []) listener({ data, lastEventId });
  }
}

afterEach(() => vi.useRealTimers());

describe("task event subscription", () => {
  it("resumes from the last cursor and suppresses duplicate events", () => {
    const source = new FakeSource("/ui/api/tasks/task-1/events/stream");
    const factory = vi.fn(() => source);
    const events: string[] = [];
    const subscription = subscribeToTaskEvents("task-1", { onEvent: (event) => events.push(event.id) }, factory);

    source.emit("task.running", JSON.stringify({ id: "event-1", type: "task.running", created_at: "now" }));
    source.emit("message", JSON.stringify({ id: "event-1", type: "task.running", created_at: "now" }));
    subscription.close();
    expect(events).toEqual(["event-1"]);
    expect(source.closed).toBe(true);
  });

  it("closes after a terminal event", () => {
    const source = new FakeSource("/ui/api/tasks/task-1/events/stream");
    const terminal = vi.fn();
    subscribeToTaskEvents("task-1", { onEvent: vi.fn(), onTerminal: terminal }, () => source);

    source.emit("task.succeeded", JSON.stringify({ id: "event-2", type: "task.succeeded", created_at: "now" }));
    expect(terminal).toHaveBeenCalledOnce();
    expect(source.closed).toBe(true);
  });

  it("reconnects with the cursor after a stream error", () => {
    vi.useFakeTimers();
    const first = new FakeSource("/ui/api/tasks/task-1/events/stream");
    const second = new FakeSource("/ui/api/tasks/task-1/events/stream?after=event-1");
    const factory = vi.fn().mockReturnValueOnce(first).mockReturnValueOnce(second);
    subscribeToTaskEvents("task-1", { onEvent: vi.fn() }, factory);
    first.emit("task.running", JSON.stringify({ id: "event-1", type: "task.running", created_at: "now" }));
    first.onerror?.();
    vi.advanceTimersByTime(250);
    expect(factory).toHaveBeenCalledTimes(2);
    expect(second.url).toBe("/ui/api/tasks/task-1/events/stream?after=event-1");
  });
});
