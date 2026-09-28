import { afterEach, describe, expect, it, vi } from "vitest";
import { approveAction, createSession, getRoutingStatus, interruptTurn, listSessions, sendTurn, subscribeToSession } from "./api";

afterEach(() => vi.unstubAllGlobals());

describe("local Codex session API", () => {
  it("loads routing state and sessions only from the same-origin host", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ mode: "normal", observed_mode: "normal", observed_at: "2026-09-29T00:00:00Z", generation: 3, fresh: true, published: true, can_start: true, fallback_enabled: false }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ sessions: [{ id: "thread-1", provider: "openai", model: "gpt-6-luna", effort: "high", mode: "normal", generation: 3, created_at: "2026-09-29T00:00:00Z" }] }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(getRoutingStatus()).resolves.toMatchObject({ mode: "normal", can_start: true });
    await expect(listSessions()).resolves.toHaveLength(1);
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(["/api/status", "/api/sessions"]);
    expect(fetchMock.mock.calls.every(([, init]) => init?.credentials === "same-origin")).toBe(true);
  });

  it("creates a session with only the selected directory and user prompt", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ session: { id: "thread-1", provider: "openai", model: "gpt-6-luna", effort: "high", mode: "normal", generation: 3, created_at: "2026-09-29T00:00:00Z" }, turn_id: "turn-1" }), { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);

    await createSession("C:/src/project", "Inspect the repository");
    expect(fetchMock).toHaveBeenCalledWith("/api/sessions", expect.objectContaining({
      method: "POST",
      credentials: "same-origin",
      body: JSON.stringify({ cwd: "C:/src/project", prompt: "Inspect the repository" }),
    }));
  });

  it("sends a continuation once and never retries an ambiguous response", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("", { status: 502 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(sendTurn("thread/1", "continue" )).rejects.toMatchObject({ status: 502 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/sessions/thread%2F1/turns");
  });

  it("posts only an explicit one-shot approval decision", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await approveAction("thread-1", "approval-id", "decline");
    expect(fetchMock).toHaveBeenCalledWith("/api/approvals", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ thread_id: "thread-1", id: "approval-id", decision: "decline" }),
    }));
  });

  it("rejects session-wide approval decisions without sending them", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    await expect(approveAction("thread-1", "approval-id", "acceptForSession")).rejects.toMatchObject({ status: 400 });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("interrupts only the specified active turn", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await interruptTurn("thread-1", "turn-9");
    expect(fetchMock.mock.calls[0][0]).toBe("/api/sessions/thread-1/interrupt");
    expect(JSON.parse(fetchMock.mock.calls[0][1].body as string)).toEqual({ turn_id: "turn-9" });
  });

  it("renders only same-thread events and leaves reconnect to EventSource without resending turns", () => {
    const handlers = new Map<string, (event: { data: string }) => void>();
    const source = { addEventListener: vi.fn((type: string, listener: (event: MessageEvent<string>) => void) => handlers.set(type, listener as (event: { data: string }) => void)), close: vi.fn(), onerror: null as ((event: Event) => void) | null, onopen: null as ((event: Event) => void) | null };
    const onNotice = vi.fn();
    const onConnection = vi.fn();
    const subscription = subscribeToSession("thread-1", onNotice, () => source, onConnection);
    handlers.get("message")?.({ data: JSON.stringify({ type: "assistant_delta", thread_id: "thread-1", turn_id: "turn-1", item_id: "item-1", delta: "hello" }) });
    handlers.get("message")?.({ data: JSON.stringify({ type: "assistant_delta", thread_id: "thread-2", turn_id: "turn-1", item_id: "item-2", delta: "must not render" }) });
    handlers.get("message")?.({ data: JSON.stringify({ type: "item/reasoning/summaryTextDelta", thread_id: "thread-1", delta: "hidden" }) });
    handlers.get("message")?.({ data: JSON.stringify({ type: "assistant_delta", thread_id: "thread-1", turn_id: "turn-1", item_id: "large", delta: "x".repeat(33 * 1024) }) });
    source.onerror?.(new Event("error"));

    expect(source.addEventListener).toHaveBeenCalledWith("message", expect.any(Function));
    expect(onNotice).toHaveBeenCalledWith({ type: "assistant_delta", thread_id: "thread-1", turn_id: "turn-1", item_id: "item-1", delta: "hello" });
    expect(onNotice).toHaveBeenCalledTimes(1);
    expect(onConnection).toHaveBeenLastCalledWith("reconnecting");
    expect(source.close).not.toHaveBeenCalled();
    expect(subscription.close).toBeTypeOf("function");
    expect(source.close).not.toHaveBeenCalled();
    subscription.close();
    expect(source.close).toHaveBeenCalledOnce();
  });

  it("does not expose an upstream error body in the UI error", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("secret prompt and token", { status: 503 })));
    await expect(getRoutingStatus()).rejects.toMatchObject({ message: "The local Codex session service is unavailable." });
  });
});
