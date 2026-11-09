// The one mapping from the agent's run events to the AI SDK's messages.
// reduceTurn folds a turn's stored events into message parts, for a
// conversation read back from history; TurnChunker turns the same events,
// as the task stream sends them, into the chunks useChat builds those parts
// from. Both follow the same rules, and a test holds them to it.
//
// The rules: token text joins into one text part until a step, a tool call
// or a request ends it (a withheld token adds nothing); a step starts a new
// step; a tool call adds a tool part keyed by its call ID, and its result
// settles that part; a request adds a data-request part; a safety stop
// takes out the step's text and adds a data-blocked part; an error adds a
// data-error part. Everything else (status, run_started, checkpoint,
// suspended, incomplete, done) adds nothing.
import type { DynamicToolUIPart, UIMessage, UIMessageChunk } from "ai"

import type { Event, History } from "@/client/forge/loomplane/types"
import type { PageContext } from "@/lib/page-context"
import { type Risk, riskOf } from "@/lib/risk"

/** The fixed line a safety stop shows in place of the step's text. */
export const SAFETY_STOPPED = "Loomplane's safety checks stopped this answer."

/** One event of a run, as the task stream and the history carry it. */
export type AgentEvent = {
  task_id: string
  seq: number
  kind: string
  data: Record<string, unknown>
  at: string
}

/** What a turn's messages carry besides their parts. */
export type AgentMetadata = {
  /** The turn's task; empty on a message not yet sent. */
  taskId: string
  /** What the person was looking at when they sent it, on their message. */
  context?: PageContext
  /** The turn's task status, on the agent's message. */
  status?: string
  /** Why the turn failed, when it did. */
  error?: string
}

/** The agent's own message parts, beside text and tool steps. */
export type AgentData = {
  request: { requestId: string; kind: string }
  blocked: { message: string }
  error: { message: string }
}

export type AgentMessage = UIMessage<AgentMetadata, AgentData>
export type AgentPart = AgentMessage["parts"][number]
export type AgentChunk = UIMessageChunk<AgentMetadata, AgentData>

export type ToolOutcome = "ok" | "failed" | "denied"

/** What a tool part's toolMetadata holds. */
export type ToolMeta = {
  risk: Risk
  /** Whether the server kept the call's arguments (secret_or_external
   * operations keep none). */
  stored: boolean
  outcome?: ToolOutcome
}

/** toolMeta reads a tool part's metadata back. */
export function toolMeta(part: DynamicToolUIPart): ToolMeta {
  const m = part.toolMetadata ?? {}
  const outcome = m.outcome
  return {
    risk: riskOf(m.risk),
    stored: m.stored === true,
    ...(outcome === "ok" || outcome === "failed" || outcome === "denied"
      ? { outcome }
      : {}),
  }
}

const str = (v: unknown): string => (typeof v === "string" ? v : "")

/** fromApi reads an event the generated client decoded. Its data is the
 * event's JSON object, which the client's types call a string. */
export function fromApi(e: Event): AgentEvent {
  return {
    task_id: e.taskId,
    seq: e.seq,
    kind: e.kind,
    data: dataOf(e.data),
    at: e.at,
  }
}

/** dataOf is an event's data as an object, whether it came parsed or not. */
export function dataOf(raw: unknown): Record<string, unknown> {
  if (typeof raw === "string") {
    try {
      return dataOf(JSON.parse(raw))
    } catch {
      return {}
    }
  }
  return raw && typeof raw === "object" && !Array.isArray(raw)
    ? (raw as Record<string, unknown>)
    : {}
}

type ToolCall = {
  toolName: string
  toolCallId: string
  input: unknown
  meta: ToolMeta
}

function toolCall(e: AgentEvent): ToolCall {
  const stored = Object.hasOwn(e.data, "arguments")
  return {
    toolName: str(e.data.tool) || "tool",
    toolCallId: str(e.data.call_id) || `call-${e.seq}`,
    input: stored ? e.data.arguments : undefined,
    meta: { risk: riskOf(e.data.risk), stored },
  }
}

function outcomeOf(e: AgentEvent): {
  outcome: ToolOutcome
  message: string
} {
  const o = str(e.data.outcome)
  const outcome: ToolOutcome = o === "failed" || o === "denied" ? o : "ok"
  return { outcome, message: str(e.data.message) }
}

/** The error text of a step that failed or was refused. */
function errorText(outcome: ToolOutcome, message: string): string {
  if (message) return message
  return outcome === "denied"
    ? "Loomplane refused this step."
    : "This step failed."
}

/** The step a safety stop names, when it names one. */
function stepOf(e: AgentEvent): number | undefined {
  const n = e.data.step_seq
  return typeof n === "number" && n > 0 ? n : undefined
}

const requestPart = (e: AgentEvent) => ({
  type: "data-request" as const,
  id: `request-${str(e.data.request_id)}`,
  data: { requestId: str(e.data.request_id), kind: str(e.data.kind) },
})

const blockedPart = (e: AgentEvent) => ({
  type: "data-blocked" as const,
  id: `blocked-${e.seq}`,
  data: { message: SAFETY_STOPPED },
})

const errorPart = (e: AgentEvent) => ({
  type: "data-error" as const,
  id: `error-${e.seq}`,
  data: { message: str(e.data.message) || "The agent stopped on an error." },
})

/** reduceTurn folds a turn's events into its message parts. */
export function reduceTurn(events: readonly AgentEvent[]): AgentPart[] {
  let parts: AgentPart[] = []
  let text: { type: "text"; text: string; state: "done" } | undefined
  // Each step's boundary part, by the seq of its step event.
  const steps = new Map<number, AgentPart>()
  for (const e of events) {
    switch (e.kind) {
      case "token": {
        const t = str(e.data.text)
        if (e.data.withheld === true || !t) break
        if (text) text.text += t
        else {
          text = { type: "text", text: t, state: "done" }
          parts.push(text)
        }
        break
      }
      case "step": {
        text = undefined
        const start: AgentPart = { type: "step-start" }
        steps.set(e.seq, start)
        parts.push(start)
        break
      }
      case "tool_call": {
        text = undefined
        const c = toolCall(e)
        parts.push({
          type: "dynamic-tool",
          toolName: c.toolName,
          toolCallId: c.toolCallId,
          state: "input-available",
          input: c.input,
          toolMetadata: c.meta,
        })
        break
      }
      case "tool_result": {
        const id = str(e.data.call_id)
        const at = parts.findLastIndex(
          (p) => p.type === "dynamic-tool" && p.toolCallId === id
        )
        if (at < 0) break
        const call = parts[at] as DynamicToolUIPart
        const { outcome, message } = outcomeOf(e)
        const toolMetadata = { ...toolMeta(call), outcome }
        const base = {
          type: "dynamic-tool" as const,
          toolName: call.toolName,
          toolCallId: call.toolCallId,
          input: call.input,
          toolMetadata,
        }
        parts[at] =
          outcome === "ok"
            ? {
                ...base,
                state: "output-available",
                output: message || undefined,
              }
            : {
                ...base,
                state: "output-error",
                errorText: errorText(outcome, message),
              }
        break
      }
      case "request":
        text = undefined
        parts.push(requestPart(e))
        break
      case "safety_block": {
        // The stop names the step it withheld (step_seq); the server only
        // ever names the latest, and without one the latest is meant.
        text = undefined
        const named = stepOf(e)
        const start =
          (named !== undefined ? steps.get(named) : undefined) ??
          parts.findLast((p) => p.type === "step-start")
        const from = start ? parts.indexOf(start) + 1 : 0
        const next = parts.findIndex(
          (p, i) => i >= from && p.type === "step-start"
        )
        const to = next < 0 ? parts.length : next
        parts = [
          ...parts.slice(0, from),
          ...parts.slice(from, to).filter((p) => p.type !== "text"),
          ...parts.slice(to),
          blockedPart(e),
        ]
        break
      }
      case "error":
        text = undefined
        parts.push(errorPart(e))
        break
    }
  }
  return parts
}

type ToolStep = { kind: "tool"; call: ToolCall; result?: AgentChunk }

/**
 * TurnChunker turns a turn's events, in order, into the chunks of its
 * message. It keeps the step's tool calls and requests, so a safety stop
 * can reset the step and put them back without the stopped text.
 */
export class TurnChunker {
  private textId: string | undefined
  // The seq of the step event the stream is in.
  private step: number | undefined
  // The turn's calls by ID, and what the current step has added.
  private calls = new Map<string, ToolStep>()
  private stepParts: (ToolStep | { kind: "data"; chunk: AgentChunk })[] = []

  private closeText(): AgentChunk[] {
    if (!this.textId) return []
    const id = this.textId
    this.textId = undefined
    return [{ type: "text-end", id }]
  }

  /** eventToChunks is what one event adds to the message. */
  eventToChunks(e: AgentEvent): AgentChunk[] {
    switch (e.kind) {
      case "token": {
        const delta = str(e.data.text)
        if (e.data.withheld === true || !delta) return []
        const out: AgentChunk[] = []
        if (!this.textId) {
          this.textId = `text-${e.seq}`
          out.push({ type: "text-start", id: this.textId })
        }
        out.push({ type: "text-delta", id: this.textId, delta })
        return out
      }
      case "step":
        this.stepParts = []
        this.step = e.seq
        return [...this.closeText(), { type: "start-step" }]
      case "tool_call": {
        const step: ToolStep = { kind: "tool", call: toolCall(e) }
        this.calls.set(step.call.toolCallId, step)
        this.stepParts.push(step)
        return [...this.closeText(), inputChunk(step.call)]
      }
      case "tool_result": {
        const id = str(e.data.call_id)
        const step = this.calls.get(id)
        if (!step) return []
        const { outcome, message } = outcomeOf(e)
        step.call.meta = { ...step.call.meta, outcome }
        step.result =
          outcome === "ok"
            ? {
                type: "tool-output-available",
                toolCallId: id,
                output: message || undefined,
                dynamic: true,
                toolMetadata: step.call.meta,
              }
            : {
                type: "tool-output-error",
                toolCallId: id,
                errorText: errorText(outcome, message),
                dynamic: true,
                toolMetadata: step.call.meta,
              }
        return [step.result]
      }
      case "request": {
        const chunk = requestPart(e)
        this.stepParts.push({ kind: "data", chunk })
        return [...this.closeText(), chunk]
      }
      case "safety_block": {
        // The step's text is already on screen: reset the step, put its
        // calls and requests back, and say why the rest is gone. The stop
        // names its step (step_seq), and the server only names the latest:
        // the one a reset takes back. A stop naming an earlier step, which
        // a stream can't take back, only says it stopped.
        const named = stepOf(e)
        if (
          named !== undefined &&
          this.step !== undefined &&
          named !== this.step
        ) {
          const blocked = blockedPart(e)
          this.stepParts.push({ kind: "data", chunk: blocked })
          return [...this.closeText(), blocked]
        }
        this.textId = undefined
        const out: AgentChunk[] = [{ type: "reset-step" }]
        for (const p of this.stepParts) {
          if (p.kind === "data") out.push(p.chunk)
          else {
            out.push(inputChunk({ ...p.call, meta: toolMetaWithout(p.call) }))
            if (p.result) out.push(p.result)
          }
        }
        const blocked = blockedPart(e)
        // Kept with the step, so a second stop in it puts this one back too.
        this.stepParts.push({ kind: "data", chunk: blocked })
        out.push(blocked)
        return out
      }
      case "error":
        return [...this.closeText(), errorPart(e)]
      default:
        return []
    }
  }

  /** finish closes the text still open when the turn's stream ends. */
  finish(): AgentChunk[] {
    return this.closeText()
  }
}

// A replayed call starts as it did, without the outcome its result adds.
function toolMetaWithout(call: ToolCall): ToolMeta {
  return { risk: call.meta.risk, stored: call.meta.stored }
}

function inputChunk(call: ToolCall): AgentChunk {
  return {
    type: "tool-input-available",
    toolCallId: call.toolCallId,
    toolName: call.toolName,
    input: call.input,
    dynamic: true,
    toolMetadata: toolMetaWithout(call),
  }
}

/** contextOf reads the page a turn's message was sent from, as the
 * server kept it, when it kept one. */
function contextOf(turn: History["turns"][number]): { context?: PageContext } {
  const c = (turn as { context?: unknown }).context
  if (!c || typeof c !== "object") return {}
  const { label, path, kind } = c as Record<string, unknown>
  if (typeof label !== "string" || typeof path !== "string") return {}
  return {
    context: {
      ...(c as PageContext),
      kind: typeof kind === "string" ? (kind as PageContext["kind"]) : "page",
    },
  }
}

/** historyToMessages is a conversation's turns as useChat's messages: the
 * person's message, then the agent's turn on it. */
export function historyToMessages(history: History): AgentMessage[] {
  return history.turns.flatMap((turn): AgentMessage[] => [
    {
      id: `user-${turn.taskId}`,
      role: "user",
      parts: [{ type: "text", text: turn.message }],
      metadata: { taskId: turn.taskId, ...contextOf(turn) },
    },
    {
      id: `agent-${turn.taskId}`,
      role: "assistant",
      parts: reduceTurn(turn.events.map(fromApi)),
      metadata: {
        taskId: turn.taskId,
        status: turn.status,
        ...(turn.error ? { error: turn.error } : {}),
      },
    },
  ])
}
