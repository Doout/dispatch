// @vitest-environment jsdom
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { useWorkflowLogs } from "./useWorkflowLogs";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

function logStreams() {
  const streams: ReadableStreamDefaultController<Uint8Array>[] = [];
  const signals: AbortSignal[] = [];
  const fetcher = vi.fn(async (_url, options) => {
    signals.push(options.signal);
    return new Response(new ReadableStream<Uint8Array>({ start(controller) { streams.push(controller); } }), { headers: { "Content-Type": "text/event-stream" } });
  });
  vi.stubGlobal("fetch", fetcher);
  const send = (index: number, event: string, data: unknown) => streams[index].enqueue(new TextEncoder().encode(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`));
  return { fetcher, streams, signals, send };
}

it("keeps concurrent job deltas separate and handles split Unicode output and replacements", async () => {
  const stream = logStreams();
  const { result } = renderHook(() => useWorkflowLogs("active-run"));
  await waitFor(() => expect(stream.fetcher).toHaveBeenCalledTimes(1));
  await act(async () => {
    stream.send(0, "connected", null);
    const bytes = new TextEncoder().encode(`event: log\ndata: ${JSON.stringify({ id: "api", name: "build-api", state: "running", error: "", keep: 0, text: "🚀 build" })}\n\n`);
    const split = bytes.indexOf(0xf0) + 2;
    stream.streams[0].enqueue(bytes.slice(0, split));
    stream.streams[0].enqueue(bytes.slice(split));
    stream.send(0, "log", { id: "ui", name: "build-ui", state: "running", error: "", keep: 0, text: "Installing" });
    stream.send(0, "log", { id: "api", name: "build-api", state: "running", error: "", keep: 8, text: "\nnext" });
    stream.send(0, "log", { id: "ui", name: "build-ui", state: "failed", error: "Failed", keep: 0, text: "[redacted]" });
  });
  expect(result.current.jobs.api.text).toBe("🚀 build\nnext");
  expect(result.current.jobs.ui.text).toBe("[redacted]");
  expect(result.current.jobs.ui.state).toBe("failed");
  expect(result.current.status).toBe("Live");
});

it("finishes an active stream after metadata completes and resets when opening saved history", async () => {
  const stream = logStreams();
  const { result, rerender, unmount } = renderHook(({ id, active }) => useWorkflowLogs(id, active), { initialProps: { id: "running", active: true } });
  await waitFor(() => expect(stream.fetcher).toHaveBeenCalledTimes(1));
  await act(async () => stream.send(0, "log", { id: "job", name: "build", state: "running", error: "", keep: 0, text: "Building" }));
  rerender({ id: "running", active: false });
  expect(stream.signals[0].aborted).toBe(false);
  await act(async () => {
    stream.send(0, "log", { id: "job", name: "build", state: "succeeded", error: "", keep: 8, text: "\nPushed" });
    stream.send(0, "complete", "succeeded");
  });
  expect(result.current.jobs.job.text).toBe("Building\nPushed");
  expect(result.current.status).toBe("Run succeeded");
  rerender({ id: "history", active: false });
  expect(result.current.jobs).toEqual({});
  expect(result.current.status).toBe("Saved output");
  expect(stream.signals[0].aborted).toBe(true);
  expect(stream.fetcher).toHaveBeenCalledTimes(1);
  unmount();
});

it("retains output on disconnect and replaces it with the reconnect replay without duplication", async () => {
  const stream = logStreams();
  const { result, unmount } = renderHook(() => useWorkflowLogs("running"));
  await waitFor(() => expect(stream.fetcher).toHaveBeenCalledTimes(1));
  await act(async () => stream.send(0, "log", { id: "job", name: "build", state: "running", error: "", keep: 0, text: "Building" }));
  await act(async () => stream.streams[0].close());
  expect(result.current.phase).toBe("error");
  act(() => result.current.reconnect());
  expect(result.current.jobs.job.text).toBe("Building");
  await waitFor(() => expect(stream.fetcher).toHaveBeenCalledTimes(2));
  await act(async () => {
    stream.send(1, "connected", null);
    stream.send(1, "log", { id: "job", name: "build", state: "running", error: "", keep: 0, text: "Building\nPushing" });
  });
  expect(result.current.jobs.job.text).toBe("Building\nPushing");
  expect(result.current.phase).toBe("live");
  expect(stream.signals[0].aborted).toBe(true);
  unmount();
  expect(stream.signals[1].aborted).toBe(true);
});

it("ignores a late response from a previous resource and never streams completed history", async () => {
  let finishOld!: (response: Response) => void;
  const cancel = vi.fn();
  const fetcher = vi.fn(() => new Promise<Response>(resolve => { finishOld = resolve; }));
  vi.stubGlobal("fetch", fetcher);
  const { result, rerender } = renderHook(({ scope, active }) => useWorkflowLogs("revision", active, scope), { initialProps: { scope: "old-resource", active: true } });
  rerender({ scope: "new-resource", active: false });
  await act(async () => finishOld(new Response(new ReadableStream({
    start(controller) { controller.enqueue(new TextEncoder().encode('event: log\ndata: {"id":"old","name":"old","state":"running","keep":0,"text":"Old output"}\n\n')); },
    cancel,
  }), { headers: { "Content-Type": "text/event-stream" } })));
  expect(result.current.jobs).toEqual({});
  expect(result.current.phase).toBe("idle");
  expect(cancel).toHaveBeenCalledTimes(1);
  expect(fetcher).toHaveBeenCalledTimes(1);
});
