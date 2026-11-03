import { readUIMessageStream, type UIMessageChunk } from "ai"
import { expect, test } from "vitest"

import {
  type AgentEvent,
  type AgentMessage,
  type AgentPart,
  historyToMessages,
  reduceTurn,
  SAFETY_STOPPED,
  TurnChunker,
} from "./agent-events"

let seq = 0
function ev(kind: string, data: Record<string, unknown> = {}): AgentEvent {
  seq += 1
  return { task_id: "t1", seq, kind, data, at: "2026-11-03T10:00:00Z" }
}

// The parts a person sees: step boundaries draw nothing.
const shown = (parts: AgentPart[]) =>
  parts.filter((p) => p.type !== "step-start")

// live replays events through the chunker and the AI SDK's own reader, as
// useChat does with the task stream.
async function live(events: AgentEvent[]): Promise<AgentPart[]> {
  const chunker = new TurnChunker()
  const chunks: UIMessageChunk[] = [{ type: "start", messageId: "agent-t1" }]
  for (const e of events) chunks.push(...chunker.eventToChunks(e))
  chunks.push(...chunker.finish(), { type: "finish" })
  const stream = new ReadableStream<UIMessageChunk>({
    start(controller) {
      for (const c of chunks) controller.enqueue(c)
      controller.close()
    },
  })
  let last: AgentMessage | undefined
  for await (const m of readUIMessageStream<AgentMessage>({ stream })) last = m
  return last?.parts ?? []
}

test("reduceTurn joins tokens into text until a step", async () => {
  seq = 0
  const events = [
    ev("run_started"),
    ev("step", { index: 0 }),
    ev("token", { text: "Two " }),
    ev("token", { text: "projects." }),
    ev("step", { index: 1 }),
    ev("token", { text: "Both are live." }),
    ev("checkpoint"),
    ev("done"),
    ev("status", { status: "completed" }),
  ]
  const parts = reduceTurn(events)
  expect(shown(parts)).toEqual([
    { type: "text", text: "Two projects.", state: "done" },
    { type: "text", text: "Both are live.", state: "done" },
  ])
  expect(await live(events)).toEqual(parts)
})

test("reduceTurn pairs tool results with their calls", async () => {
  seq = 0
  const events = [
    ev("step"),
    ev("tool_call", {
      tool: "get_project",
      call_id: "c1",
      risk: "read",
      arguments: { project: "api" },
    }),
    ev("tool_call", {
      tool: "get_project",
      call_id: "c2",
      risk: "read",
      arguments: { project: "web" },
    }),
    ev("tool_result", { tool: "get_project", call_id: "c1", outcome: "ok" }),
    ev("tool_result", {
      tool: "get_project",
      call_id: "c2",
      outcome: "failed",
      message: "project web not found",
    }),
    ev("tool_call", {
      tool: "set_variable",
      call_id: "c3",
      risk: "secret_or_external",
    }),
    ev("tool_result", {
      tool: "set_variable",
      call_id: "c3",
      outcome: "denied",
      message: "a person has to set secrets",
    }),
  ]
  const parts = reduceTurn(events)
  expect(shown(parts)).toEqual([
    expect.objectContaining({
      type: "dynamic-tool",
      toolName: "get_project",
      toolCallId: "c1",
      state: "output-available",
      input: { project: "api" },
      toolMetadata: { risk: "read", stored: true, outcome: "ok" },
    }),
    expect.objectContaining({
      toolCallId: "c2",
      state: "output-error",
      input: { project: "web" },
      errorText: "project web not found",
      toolMetadata: { risk: "read", stored: true, outcome: "failed" },
    }),
    expect.objectContaining({
      toolCallId: "c3",
      state: "output-error",
      errorText: "a person has to set secrets",
      toolMetadata: {
        risk: "secret_or_external",
        stored: false,
        outcome: "denied",
      },
    }),
  ])
  expect(await live(events)).toEqual(parts)
})

test("reduceTurn withholds a blocked step", async () => {
  seq = 0
  // Live: the stopped step's tokens came before the stop.
  const streamed = [
    ev("step"),
    ev("token", { text: "Hello" }),
    ev("step"),
    ev("tool_call", {
      tool: "list_projects",
      call_id: "c1",
      risk: "read",
      arguments: {},
    }),
    ev("tool_result", { tool: "list_projects", call_id: "c1", outcome: "ok" }),
    ev("token", { text: "AKIA" }),
    ev("token", { text: "IOSFODNN7EXAMPLE" }),
    ev("safety_block", { message: SAFETY_STOPPED, step_seq: 3 }),
  ]
  const parts = reduceTurn(streamed)
  expect(shown(parts)).toEqual([
    { type: "text", text: "Hello", state: "done" },
    expect.objectContaining({ toolCallId: "c1", state: "output-available" }),
    {
      type: "data-blocked",
      id: "blocked-8",
      data: { message: SAFETY_STOPPED },
    },
  ])
  expect(JSON.stringify(parts)).not.toContain("AKIA")
  expect(await live(streamed)).toEqual(parts)

  // From history: the server already withheld the step's tokens.
  seq = 0
  const stored = [
    ev("step"),
    ev("token", { text: "Hello" }),
    ev("step"),
    ev("token", { text: "", withheld: true }),
    ev("safety_block", { message: SAFETY_STOPPED, step_seq: 3 }),
  ]
  expect(shown(reduceTurn(stored))).toEqual([
    { type: "text", text: "Hello", state: "done" },
    {
      type: "data-blocked",
      id: "blocked-5",
      data: { message: SAFETY_STOPPED },
    },
  ])
})

test("reduceTurn shows requests and errors", async () => {
  seq = 0
  const events = [
    ev("step"),
    ev("token", { text: "Which region?" }),
    ev("request", { request_id: "areq_1", kind: "answer" }),
    ev("suspended", { reason: "waiting" }),
    ev("run_started", { resumed: true }),
    ev("step"),
    ev("error", { message: "the provider timed out" }),
  ]
  const parts = reduceTurn(events)
  expect(shown(parts)).toEqual([
    { type: "text", text: "Which region?", state: "done" },
    {
      type: "data-request",
      id: "request-areq_1",
      data: { requestId: "areq_1", kind: "answer" },
    },
    {
      type: "data-error",
      id: "error-7",
      data: { message: "the provider timed out" },
    },
  ])
  expect(await live(events)).toEqual(parts)
})

test("historyToMessages pairs each message with the agent's turn", () => {
  const messages = historyToMessages({
    conversation: {
      id: "conv_1",
      orgId: "org",
      projectId: "",
      title: "What's deployed?",
      startedBy: "usr_1",
      status: "failed",
      lastTaskId: "t1",
      turns: 1,
      createdAt: "2026-11-03T10:00:00Z",
      updatedAt: "2026-11-03T10:00:00Z",
    },
    more: false,
    turns: [
      {
        taskId: "t1",
        message: "What's deployed?",
        status: "failed",
        error: "out of budget",
        eventSeq: 2,
        createdAt: "2026-11-03T10:00:00Z",
        // The generated type says string; the server sends the object.
        events: [
          {
            taskId: "t1",
            seq: 2,
            kind: "token",
            data: { text: "Two" } as unknown as string,
            at: "2026-11-03T10:00:00Z",
          },
        ],
      },
    ],
  })
  expect(messages).toEqual([
    {
      id: "user-t1",
      role: "user",
      parts: [{ type: "text", text: "What's deployed?" }],
      metadata: { taskId: "t1" },
    },
    {
      id: "agent-t1",
      role: "assistant",
      parts: [{ type: "text", text: "Two", state: "done" }],
      metadata: { taskId: "t1", status: "failed", error: "out of budget" },
    },
  ])
})
