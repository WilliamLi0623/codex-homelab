import { afterEach, describe, expect, it, vi } from "vitest";
import { ControllerError, createTask, getTaskEvents, listTasks, retryTask, startTaskAttempt } from "./controller";

afterEach(() => vi.unstubAllGlobals());

describe("controller client", () => {
  it("loads tasks through the same-origin BFF", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ tasks: [{ id: "task-1" }] }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listTasks()).resolves.toEqual([{ id: "task-1" }]);
    expect(fetchMock).toHaveBeenCalledWith("/ui/api/tasks", expect.objectContaining({ credentials: "same-origin" }));
  });

  it("requests events after a cursor", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ events: [] }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await getTaskEvents("task/1", "event-2");
    expect(fetchMock).toHaveBeenCalledWith("/ui/api/tasks/task%2F1/events?after=event-2", expect.anything());
  });

  it("uses a generated-safe request and redacts controller error bodies", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("secret token and stack trace", { status: 503 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(createTask({ repository: "owner/repo", base_ref: "main", objective: "test", idempotency_key: "ui-1" })).rejects.toMatchObject({
      status: 503,
      errorClass: "unavailable",
      message: "The Controller is temporarily unavailable.",
    } satisfies Partial<ControllerError>);
  });

  it("submits the fixed worker model profile for new tasks", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ task: { id: "task-1" } }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await createTask({ repository: "owner/repo", base_ref: "main", objective: "test", idempotency_key: "ui-1" });

    expect(JSON.parse(fetchMock.mock.calls[0][1].body as string)).toEqual({
      repository: "owner/repo",
      base_ref: "main",
      objective: "test",
      idempotency_key: "ui-1",
      model_profile: "worker",
    });
  });

  it("submits the fixed worker model profile for new attempts", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ task: { id: "task-1" }, attempt: { id: "attempt-1" } }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await startTaskAttempt("task-1");

    expect(JSON.parse(fetchMock.mock.calls[0][1].body as string)).toEqual({ model_profile: "worker" });
  });

  it("submits the fixed worker model profile when retrying a task", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ task: { id: "task-1" }, attempt: { id: "attempt-2" } }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await retryTask("task-1");

    expect(JSON.parse(fetchMock.mock.calls[0][1].body as string)).toEqual({ model_profile: "worker" });
  });
});
