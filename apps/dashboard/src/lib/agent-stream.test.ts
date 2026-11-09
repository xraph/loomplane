import type { UIMessageChunk } from "ai"
import { expect, test } from "vitest"

import { FakeEventSource } from "@/test-fixtures/event-source"
import { AgentChatTransport } from "./agent-stream"

function transport() {
  return new AgentChatTransport({
    start: async () => {
      throw new Error("not in this test")
    },
    send: async () => {
      throw new Error("not in this test")
    },
  })
}

function frame(seq: number, text: string) {
  return {
    task_id: "t1",
    seq,
    kind: "token",
    data: { text },
    at: "2026-11-03T10:00:00Z",
  }
}

async function readAll(stream: ReadableStream<UIMessageChunk>) {
  const out: UIMessageChunk[] = []
  const reader = stream.getReader()
  for (;;) {
    const { done, value } = await reader.read()
    if (done) return out
    out.push(value)
  }
}

test("the transport opens the task's stream after the given seq", async () => {
  const t = transport()
  t.resumeFrom({ taskId: "t1", eventSeq: 5, status: "running", events: [] })
  const stream = await t.reconnectToStream({ chatId: "conv_1" })
  expect(stream).not.toBeNull()
  const source = FakeEventSource.latest()
  expect(source.url).toMatch(/\/api\/v1\/agent\/tasks\/t1\/stream\?after=5$/)
  expect(source.withCredentials).toBe(true)
})

test("the transport has nothing to resume once the turn is over", async () => {
  const t = transport()
  t.resumeFrom({ taskId: "t1", eventSeq: 9, status: "completed", events: [] })
  expect(await t.reconnectToStream({ chatId: "conv_1" })).toBeNull()
  expect(FakeEventSource.instances).toHaveLength(0)
})

test("the transport ignores events it already has", async () => {
  const t = transport()
  t.resumeFrom({ taskId: "t1", eventSeq: 5, status: "running", events: [] })
  const stream = await t.reconnectToStream({ chatId: "conv_1" })
  const source = FakeEventSource.latest()
  source.open()
  source.emit("agent.event", frame(6, "Two "), 6)
  source.emit("agent.event", frame(7, "projects"), 7)
  // The connection drops; the browser reconnects with Last-Event-ID and
  // the server sends 7 again.
  source.fail()
  source.open()
  source.emit("agent.event", frame(7, "projects"), 7)
  source.emit("agent.event", frame(8, "."), 8)
  source.emit("agent.end", { status: "completed" })
  const chunks = await readAll(stream as ReadableStream<UIMessageChunk>)
  const text = chunks
    .filter((c) => c.type === "text-delta")
    .map((c) => (c as { delta: string }).delta)
  expect(text).toEqual(["Two ", "projects", "."])
})

test("the transport closes on agent.end", async () => {
  const t = transport()
  t.resumeFrom({ taskId: "t1", eventSeq: 0, status: "running", events: [] })
  const stream = await t.reconnectToStream({ chatId: "conv_1" })
  const source = FakeEventSource.latest()
  source.emit("agent.event", frame(1, "Done."), 1)
  source.emit("agent.end", { status: "completed" })
  const chunks = await readAll(stream as ReadableStream<UIMessageChunk>)
  expect(source.closed).toBe(true)
  expect(chunks.at(-1)).toEqual({ type: "finish" })
  expect(chunks).toContainEqual({
    type: "message-metadata",
    messageMetadata: { taskId: "t1", status: "completed" },
  })
  expect(t.openTask()).toBeUndefined()
})
