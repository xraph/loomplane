// useChat's way to the workspace's agent. A message goes to the
// conversation routes, which start a task; the agent's answer is that
// task's event stream, turned into UI message chunks by TurnChunker. A
// conversation opened with a turn still running picks its stream up where
// the history left off (useChat's resume).
import type { ChatTransport } from "ai"

import { op_agentTasks_stream } from "@/client/forge/loomplane/ops/agentTasks.stream"
import type { Sent } from "@/client/forge/loomplane/types"
import {
  type AgentChunk,
  type AgentEvent,
  type AgentMessage,
  dataOf,
  TurnChunker,
} from "@/lib/agent-events"
import { useAgent, useFeatures } from "@/lib/features"
import { useOrganization } from "@/lib/organization"

/**
 * useAssistantAvailable says whether this person can talk to the agent: the
 * server has AI on, the workspace has its agent on, and they may use it.
 */
export function useAssistantAvailable(): boolean {
  const { ai } = useFeatures()
  const agent = useAgent()
  const { can } = useOrganization()
  return ai && agent.enabled && can("agent:use")
}

/** A turn whose task may still be running. */
export type OpenTurn = {
  taskId: string
  /** The last event seq the page has. */
  eventSeq: number
  status: string
  /** The turn's events so far, which the page already shows. */
  events: AgentEvent[]
}

/** How a task's stream ended: its final status, and the error it failed on. */
export type StreamEnd = { status: string; error?: string }

/** How the transport reaches the API, and who hears what happened. */
export type AgentChatHandlers = {
  /** start opens a conversation with its first message. */
  start: (body: { message: string; projectId?: string }) => Promise<Sent>
  /** send adds a message to a conversation. */
  send: (conversationId: string, body: { message: string }) => Promise<Sent>
  /** onSent hears of each message the server took, and its task. */
  onSent?: (sent: Sent) => void
  /** onEnd hears that a task's stream is over. */
  onEnd?: (taskId: string, end: StreamEnd) => void
}

export type AgentChatOptions = Partial<AgentChatHandlers> & {
  conversationId?: string
  /** The project a new conversation is about; none is the whole workspace. */
  projectId?: string
}

const terminal = new Set(["completed", "failed", "cancelled"])

/** Why the stream gave up, when the browser stops reconnecting. */
export const STREAM_LOST =
  "Lost the connection to the agent. Reload the page to pick its answer up again."

/**
 * AgentChatTransport is the ChatTransport useChat talks to the agent
 * through. It sends only the newest message: the server keeps the
 * conversation.
 */
export class AgentChatTransport implements ChatTransport<AgentMessage> {
  private options: AgentChatOptions
  private open?: OpenTurn

  constructor(options: AgentChatOptions) {
    this.options = options
  }

  /** The conversation messages go to; none until the first is sent. */
  get conversationId(): string | undefined {
    return this.options.conversationId
  }

  /** handle sets how the transport reaches the API: a component hands it
   * its current mutations after each render. */
  handle(handlers: AgentChatHandlers): void {
    this.options = { ...this.options, ...handlers }
  }

  /** setProject scopes the next new conversation to a project, or none. */
  setProject(projectId: string | undefined): void {
    this.options = { ...this.options, projectId }
  }

  /** resumeFrom records the turn a conversation was opened on, so
   * reconnectToStream can follow it if it's still running. */
  resumeFrom(turn: OpenTurn | undefined): void {
    this.open = turn
  }

  /** The task still running, if any: the one Stop cancels. */
  openTask(): { id: string; status: string } | undefined {
    if (!this.open || terminal.has(this.open.status)) return undefined
    return { id: this.open.taskId, status: this.open.status }
  }

  async sendMessages({
    messages,
    abortSignal,
  }: Parameters<ChatTransport<AgentMessage>["sendMessages"]>[0]) {
    const last = messages.at(-1)
    const message =
      last?.parts
        .map((p) => (p.type === "text" ? p.text : ""))
        .join("")
        .trim() ?? ""
    const { conversationId, projectId, start, send } = this.options
    if (!start || !send) throw new Error("The assistant isn't ready yet.")
    const sent = conversationId
      ? await send(conversationId, { message })
      : await start({
          message,
          ...(projectId ? { projectId } : {}),
        })
    this.options = { ...this.options, conversationId: sent.conversation.id }
    this.open = {
      taskId: sent.task.id,
      eventSeq: 0,
      status: sent.task.status,
      events: [],
    }
    this.options.onSent?.(sent)
    return this.follow(this.open, abortSignal)
  }

  async reconnectToStream({
    abortSignal,
  }: Parameters<ChatTransport<AgentMessage>["reconnectToStream"]>[0]) {
    const turn = this.open
    if (!turn || terminal.has(turn.status)) return null
    return this.follow(turn, abortSignal)
  }

  // A resumed turn replays the events the page has before the live ones,
  // so useChat rebuilds the agent's message whole.
  readonly resumeStreamIsReplay = true

  private follow(
    turn: OpenTurn,
    abortSignal: AbortSignal | undefined
  ): ReadableStream<AgentChunk> {
    const chunker = new TurnChunker()
    let last = turn.eventSeq
    let source: EventSource | undefined
    let done = false
    const stop = () => {
      done = true
      source?.close()
    }
    return new ReadableStream<AgentChunk>({
      start: (controller) => {
        const emit = (chunks: AgentChunk[]) => {
          for (const c of chunks) controller.enqueue(c)
        }
        const finish = (end: StreamEnd) => {
          if (done) return
          stop()
          if (this.open?.taskId === turn.taskId)
            this.open = { ...this.open, status: end.status }
          emit([
            ...chunker.finish(),
            {
              type: "message-metadata",
              messageMetadata: {
                taskId: turn.taskId,
                status: end.status,
                ...(end.error ? { error: end.error } : {}),
              },
            },
            { type: "finish" },
          ])
          controller.close()
          this.options.onEnd?.(turn.taskId, end)
        }

        emit([
          {
            type: "start",
            messageId: `agent-${turn.taskId}`,
            messageMetadata: { taskId: turn.taskId, status: turn.status },
          },
        ])
        for (const e of turn.events) emit(chunker.eventToChunks(e))

        abortSignal?.addEventListener("abort", () => {
          if (done) return
          stop()
          controller.close()
        })
        if (abortSignal?.aborted) {
          stop()
          controller.close()
          return
        }

        const path = op_agentTasks_stream.path.replace(
          "{id}",
          encodeURIComponent(turn.taskId)
        )
        const url = new URL(path, window.location.origin)
        url.searchParams.set("after", String(last))
        source = new EventSource(url, { withCredentials: true })
        source.addEventListener("agent.event", (msg) => {
          const ev = parseEvent((msg as MessageEvent<string>).data)
          // EventSource reconnects with Last-Event-ID by itself; whatever
          // the server sends again is already on the page.
          if (!ev || ev.seq <= last || done) return
          last = ev.seq
          if (this.open?.taskId === turn.taskId) {
            this.open = { ...this.open, eventSeq: ev.seq }
            if (ev.kind === "status" && typeof ev.data.status === "string")
              this.open = { ...this.open, status: ev.data.status }
          }
          emit(chunker.eventToChunks(ev))
          if (ev.kind === "status" && typeof ev.data.status === "string")
            emit([
              {
                type: "message-metadata",
                messageMetadata: {
                  taskId: turn.taskId,
                  status: ev.data.status,
                },
              },
            ])
        })
        source.addEventListener("agent.end", (msg) => {
          const data = dataOf((msg as MessageEvent<string>).data)
          finish({
            status: typeof data.status === "string" ? data.status : "",
            ...(typeof data.error === "string" && data.error
              ? { error: data.error }
              : {}),
          })
        })
        source.onerror = () => {
          // While it's connecting again the browser is on it; closed means
          // it gave up (the task is gone, or the server refused).
          if (done || source?.readyState !== EventSource.CLOSED) return
          stop()
          controller.error(new Error(STREAM_LOST))
        }
      },
      cancel: () => stop(),
    })
  }
}

function parseEvent(raw: string): AgentEvent | undefined {
  const d = dataOf(raw)
  if (typeof d.seq !== "number" || typeof d.kind !== "string") return undefined
  return {
    task_id: typeof d.task_id === "string" ? d.task_id : "",
    seq: d.seq,
    kind: d.kind,
    data: dataOf(d.data),
    at: typeof d.at === "string" ? d.at : "",
  }
}
