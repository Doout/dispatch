// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { BuildLogs } from "./BuildLogs";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("opens one stream, appends deltas and disconnects when closed", async () => {
  let stream!: ReadableStreamDefaultController<Uint8Array>;
  let signal!: AbortSignal;
  const fetchMock = vi.fn(async (_url, options) => {
    signal = options.signal;
    return new Response(new ReadableStream<Uint8Array>({ start(controller) { stream = controller; } }), { headers: { "Content-Type": "text/event-stream" } });
  });
  vi.stubGlobal("fetch", fetchMock);
  const { container } = render(<BuildLogs revisionID="running-build" />);
  expect(fetchMock).not.toHaveBeenCalled();
  const details = container.querySelector("details")!;
  details.open = true; fireEvent(details, new Event("toggle"));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  const send = (event: string, data: unknown) => stream.enqueue(new TextEncoder().encode(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`));
  await act(async () => {
    send("connected", null);
    send("log", { id: "job", name: "build-service", state: "running", error: "", keep: 0, text: "compiling" });
  });
  expect(await screen.findByText("compiling")).toBeTruthy();
  await act(async () => send("log", { id: "job", name: "build-service", state: "running", error: "", keep: 9, text: "\nfinished" }));
  expect(container.querySelector("pre")?.textContent).toBe("compiling\nfinished");
  expect(fetchMock).toHaveBeenCalledTimes(1);
  details.open = false; fireEvent(details, new Event("toggle"));
  await waitFor(() => expect(signal.aborted).toBe(true));
});
