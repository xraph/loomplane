// One conversation with the agent: the person's messages, the agent's
// answers as they stream (text, tool steps, the questions it asks), and the
// box to write the next message in. A new conversation opens on a welcome:
// the box in the middle and a few things to ask. The agent's panel shows
// it, docked, floating or across the page.
import { useChat } from "@ai-sdk/react"
import type { DynamicToolUIPart } from "ai"
import {
  CaretRightIcon,
  MapPinIcon,
  PencilSimpleIcon,
} from "@phosphor-icons/react"
import { useEffect, useState } from "react"
import { flushSync } from "react-dom"

import { messageOf } from "@/client/errors"
import {
  useAgentConversationsGet,
  useAgentConversationsList,
  useAgentConversationsSend,
  useAgentConversationsStart,
  useAgentRequestsGet,
  useAgentTasksCancel,
} from "@/client/forge/loomplane/hooks"
// Query bindings read once from a callback, not React hooks: renamed so
// nothing mistakes them for one.
import {
  useAgentConversationsGet as historyQuery,
  useAgentTasksGet as taskQuery,
} from "@/client/forge/loomplane/hooks"
import type { History, Sent, Task } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { AgentRequestCard } from "@/components/agent-request-card"
import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
} from "@/components/ai-elements/conversation"
import { Message, MessageContent } from "@/components/ai-elements/message"
import { AgentOrb, type OrbState } from "@/components/assistant/agent-orb"
import { Composer } from "@/components/assistant/composer"
import { AgentMarkdown } from "@/components/assistant/markdown"
import { ToolStep, toolLabel } from "@/components/assistant/tool-step"
import { QueryError } from "@/components/query-error"
import { CopyButton } from "@/components/runs"
import { Button } from "@/components/ui/button"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { Skeleton } from "@/components/ui/skeleton"
import {
  type AgentMessage,
  type AgentPart,
  fromApi,
  historyToMessages,
  toolMeta,
} from "@/lib/agent-events"
import {
  AgentChatTransport,
  type OpenTurn,
  STREAM_LOST,
} from "@/lib/agent-stream"
import type { PageContext } from "@/lib/page-context"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

const terminal = new Set(["completed", "failed", "cancelled"])

export const EMPTY_COPY =
  "Ask about your projects, deployments and releases. The agent reads live state with its tools and asks before anything risky."

export type ConversationViewProps = {
  /** The conversation to show; none starts a new one. */
  conversationId?: string
  /** The project a new conversation is about; none is the whole workspace. */
  projectId?: string
  /** A tighter layout, for a narrow panel. */
  compact?: boolean
  /** Center the conversation in a reading column, for a wide panel. */
  wide?: boolean
  /** What a new conversation offers to ask. */
  suggestions?: string[]
  /** What the person is looking at, sent with each message. */
  context?: PageContext
  /** Chips that say what the next message is about, shown in the box. */
  scope?: React.ReactNode
  /** onActivity hears what the agent is doing here, as the orb shows it. */
  onActivity?: (state: OrbState) => void
  /** onConversation hears the ID a new conversation got. */
  onConversation?: (id: string) => void
  /** A line under the box, such as what a new conversation is about. */
  hint?: React.ReactNode
  className?: string
}

/**
 * ConversationView shows a conversation and keeps it live. Opening another
 * conversation starts over; the one this view started keeps its stream when
 * its new ID comes back through conversationId.
 */
export function ConversationView(props: ConversationViewProps) {
  const { conversationId } = props
  // The conversation this view's own first message created.
  const [own, setOwn] = useState<string>()
  const [shown, setShown] = useState(conversationId)
  const [generation, setGeneration] = useState(0)
  if (shown !== conversationId) {
    setShown(conversationId)
    if (conversationId === undefined || conversationId !== own) {
      setOwn(undefined)
      setGeneration((g) => g + 1)
    }
  }

  const created = (id: string) => {
    // Recorded before the new ID comes back as conversationId: a router
    // update renders at once, ahead of a plain state update.
    flushSync(() => setOwn(id))
    props.onConversation?.(id)
  }

  if (conversationId && conversationId !== own)
    return (
      <WithHistory
        key={`${generation}:${conversationId}`}
        {...props}
        conversationId={conversationId}
        onCreated={created}
      />
    )
  return <ChatBody key={generation} {...props} onCreated={created} />
}

function WithHistory(
  props: ConversationViewProps & {
    conversationId: string
    onCreated: (id: string) => void
  }
) {
  const history = useQuery(useAgentConversationsGet, {
    path: { id: props.conversationId },
  })
  // History is read once, when the conversation opens: after that the
  // stream keeps it current.
  const [first, setFirst] = useState<History>()
  if (!first && history.data) setFirst(history.data)

  if (!first) {
    if (history.status === "error")
      return (
        <div className={cn("p-4", props.className)}>
          <QueryError
            error={history.error}
            fallback="This conversation couldn't be loaded."
            retry={history.refetch}
          />
        </div>
      )
    return (
      <div className={cn("grid content-start gap-3 p-4", props.className)}>
        <Skeleton className="ml-auto h-10 w-2/3 rounded-lg" />
        <Skeleton className="h-24 w-full rounded-lg" />
      </div>
    )
  }
  return <ChatBody {...props} history={first} />
}

function openTurnOf(history: History | undefined): OpenTurn | undefined {
  const last = history?.turns.at(-1)
  if (!last || terminal.has(last.status)) return undefined
  return {
    taskId: last.taskId,
    eventSeq: last.eventSeq,
    status: last.status,
    events: last.events.map(fromApi),
  }
}

function ChatBody({
  conversationId,
  projectId,
  compact,
  wide,
  suggestions = [],
  onActivity,
  context,
  scope,
  hint,
  className,
  history,
  onCreated,
}: ConversationViewProps & {
  history?: History
  onCreated: (id: string) => void
}) {
  const start = useMutation(useAgentConversationsStart)
  const send = useMutation(useAgentConversationsSend)
  const cancel = useMutation(useAgentTasksCancel)
  const invalidate = useInvalidate()
  const [draft, setDraft] = useState("")
  const [stopError, setStopError] = useState<string>()
  // The browser gave up on the stream while the task was still running.
  const [lost, setLost] = useState(false)
  const [task, setTask] = useState(() => {
    const open = openTurnOf(history)
    return open ? { id: open.taskId, status: open.status } : undefined
  })

  const [transport] = useState(() => {
    const t = new AgentChatTransport({ conversationId, projectId })
    t.resumeFrom(openTurnOf(history))
    return t
  })

  useEffect(() => {
    transport.setProject(projectId)
  }, [transport, projectId])
  useEffect(() => {
    transport.setContext(context)
  }, [transport, context])

  const [initial] = useState(() => (history ? historyToMessages(history) : []))
  const [chatId] = useState(() => conversationId)
  const {
    messages,
    sendMessage,
    status,
    error,
    setMessages,
    clearError,
    resumeStream,
  } = useChat<AgentMessage>({
    id: chatId,
    messages: initial,
    transport,
    resume: true,
    onError: (err) => {
      // A lost stream is the agent's message, not the person's: check
      // the task instead (onLost).
      if (err.message === STREAM_LOST) return
      // The server refused the message: take it back off the page and
      // put it back in the box, so it can go again.
      setMessages((ms) => {
        const last = ms.at(-1)
        if (last?.role !== "user" || last.id.startsWith("user-")) return ms
        const text = last.parts
          .map((p) => (p.type === "text" ? p.text : ""))
          .join("")
        setDraft((d) => d || text)
        return ms.slice(0, -1)
      })
    },
  })

  const streaming = status === "submitted" || status === "streaming"
  const busy = streaming || (!!task && !terminal.has(task.status))
  const [typing, setTyping] = useState(false)
  const activity = activityOf({
    busy,
    streaming,
    taskStatus: task?.status,
    last: messages.findLast((m) => m.role === "assistant"),
    typing: typing || draft !== "",
  })
  useEffect(() => {
    onActivity?.(activity)
  }, [onActivity, activity])
  useEffect(() => () => onActivity?.("idle"), [onActivity])

  const submit = (text: string) => {
    setDraft("")
    setStopError(undefined)
    void sendMessage({
      text,
      metadata: { taskId: "", ...(context ? { context } : {}) },
    })
  }

  // settle takes a task's status as the server tells it. Still running
  // with no stream, the turn can be picked up again; over, it's read back
  // from the conversation's stored turns, which have the whole answer.
  const settle = async (id: string, taskStatus: string) => {
    transport.settle(id, taskStatus)
    setTask((cur) => (cur?.id === id ? { id, status: taskStatus } : cur))
    if (!terminal.has(taskStatus)) {
      setLost(true)
      return
    }
    setLost(false)
    clearError()
    invalidate(useAgentConversationsList)
    const convo = transport.conversationId
    if (!convo) return
    try {
      const fresh = await historyQuery({ path: { id: convo } }).refetch()
      setMessages(historyToMessages(fresh))
    } catch {
      // The page keeps what it streamed; the list shows the outcome.
    }
  }

  // check reads a task whose stream the browser gave up on.
  async function check(taskId: string) {
    try {
      const t = await taskQuery({ path: { id: taskId } }).refetch()
      await settle(taskId, t.status)
    } catch {
      setLost(true)
    }
  }

  // What the transport calls back into, kept current for it. The
  // callbacks only run once a stream is under way, after this effect.
  useEffect(() => {
    transport.handle({
      start: async (body) => (await start.mutateAsync({ body })) as Sent,
      send: async (id, body) =>
        (await send.mutateAsync({ path: { id }, body })) as Sent,
      onSent: (sent) => {
        setTask({ id: sent.task.id, status: sent.task.status })
        invalidate(useAgentConversationsList)
        invalidate(useAgentConversationsGet)
        if (sent.conversation.id !== conversationId)
          onCreated(sent.conversation.id)
      },
      onEnd: (taskId, end) => {
        setTask((cur) =>
          cur?.id === taskId ? { id: taskId, status: end.status } : cur
        )
        invalidate(useAgentConversationsList)
        invalidate(useAgentConversationsGet)
      },
      onLost: (taskId) => void check(taskId),
    })
  })

  const retry = () => {
    setLost(false)
    clearError()
    void resumeStream()
  }

  const stop = async () => {
    if (!task) return
    setStopError(undefined)
    try {
      const t = (await cancel.mutateAsync({ path: { id: task.id } })) as Task
      // With a stream open, its agent.end finishes the turn; without one,
      // nothing else will.
      if (!streaming) await settle(task.id, t.status)
    } catch (err) {
      setStopError(messageOf(err, "The agent couldn't be stopped."))
    }
  }

  const problem =
    lost && busy ? (
      <>
        {STREAM_LOST}{" "}
        <button
          type="button"
          onClick={retry}
          className="font-medium underline underline-offset-2"
        >
          Retry
        </button>
      </>
    ) : (
      (stopError ?? error?.message)
    )

  const lastAgent = messages.findLast((m) => m.role === "assistant")
  const lastSent = textOf(messages.findLast((m) => m.role === "user"))
  const composer = (
    <Composer
      value={draft}
      onChange={setDraft}
      onSend={submit}
      busy={busy}
      status={status}
      onStop={() => void stop()}
      stopping={cancel.isPending}
      error={problem}
      hint={hint}
      scope={scope}
      lastSent={lastSent}
      autoFocus
      onFocusChange={setTyping}
    />
  )

  if (messages.length === 0 && !busy)
    return (
      <div
        className={cn(
          "relative flex min-h-0 flex-1 flex-col overflow-y-auto",
          className
        )}
      >
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0 bg-[radial-gradient(50%_40%_at_50%_42%,var(--brand-soft),transparent)] opacity-70"
        />
        <div
          className={cn(
            "relative m-auto grid w-full gap-5 py-10",
            compact ? "px-3" : "px-4",
            wide && "max-w-2xl"
          )}
        >
          <Welcome state={activity} />
          {composer}
          {suggestions.length > 0 && (
            <Suggestions items={suggestions} onPick={submit} />
          )}
        </div>
      </div>
    )

  const column = wide ? "mx-auto w-full max-w-3xl" : ""
  return (
    <div className={cn("flex min-h-0 flex-1 flex-col", className)}>
      <Conversation className="min-h-0">
        <ConversationContent
          className={cn(compact ? "gap-5 p-3" : "gap-6 px-4 py-5", column)}
        >
          {messages.map((m) =>
            m.role === "user" ? (
              <Message key={m.id} from="user">
                {m.metadata?.context && (
                  <SentFrom context={m.metadata.context} />
                )}
                <MessageContent className="whitespace-pre-wrap">
                  {textOf(m)}
                </MessageContent>
                <MessageActions className="justify-end opacity-0 group-hover:opacity-100 focus-within:opacity-100">
                  <CopyButton value={textOf(m)} label="Copy message" />
                  {!busy && (
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      aria-label="Edit in the box"
                      title="Edit in the box"
                      className="text-muted-foreground"
                      onClick={() => setDraft(textOf(m))}
                    >
                      <PencilSimpleIcon />
                    </Button>
                  )}
                </MessageActions>
              </Message>
            ) : (
              <AgentTurn
                key={m.id}
                message={m}
                live={busy && m === lastAgent}
              />
            )
          )}
          {streaming && messages.at(-1)?.role === "user" && (
            <Working status="queued" />
          )}
        </ConversationContent>
        <ConversationScrollButton />
      </Conversation>
      <div className={cn("shrink-0", compact ? "px-3 pb-3" : "px-4 pb-4")}>
        <div className={column}>{composer}</div>
      </div>
    </div>
  )
}

/** SentFrom says which page a message was sent from, linking back to it. */
function SentFrom({ context }: { context: PageContext }) {
  return (
    <Link
      to={context.path as "/"}
      className="ml-auto flex max-w-full items-center gap-1 truncate text-[11.5px] text-muted-foreground hover:text-foreground"
      title={`Sent from ${context.label}`}
    >
      <MapPinIcon className="size-3 shrink-0" />
      <span className="truncate">{context.label}</span>
    </Link>
  )
}

/** activityOf says what the agent is doing, for the orb. On a message,
 * it's working while its last step is a tool still running, speaking once
 * words are coming, and thinking before either; asked something, it's
 * waiting; a failed last turn is an error; otherwise it listens while you
 * write, and idles. */
export function activityOf({
  busy,
  streaming,
  taskStatus,
  last,
  typing,
}: {
  busy: boolean
  streaming: boolean
  taskStatus?: string
  last?: AgentMessage
  typing: boolean
}): OrbState {
  if (taskStatus === "waiting") return "waiting"
  if (busy) {
    const part = last?.parts.findLast((p) => p.type !== "step-start")
    if (part?.type === "dynamic-tool" && part.state === "input-available")
      return "working"
    if (streaming && part?.type === "text" && part.text) return "speaking"
    return "thinking"
  }
  if (last?.metadata?.status === "failed") return "error"
  return typing ? "listening" : "idle"
}

/** textOf joins a message's text parts. */
function textOf(m: AgentMessage | undefined): string {
  return (m?.parts ?? [])
    .map((p) => (p.type === "text" ? p.text : ""))
    .filter(Boolean)
    .join("\n\n")
}

function MessageActions({
  className,
  children,
}: {
  className?: string
  children: React.ReactNode
}) {
  return (
    <div
      className={cn(
        "-mt-1 flex items-center gap-0.5 transition-opacity",
        className
      )}
    >
      {children}
    </div>
  )
}

function Welcome({ state }: { state: OrbState }) {
  return (
    <div className="grid justify-items-center gap-2 text-center">
      <AgentOrb size={56} state={state} className="mb-1" />
      <h2 className="text-[22px] font-semibold tracking-tight">
        What can I help with?
      </h2>
      <p className="max-w-md text-[13px] text-muted-foreground">{EMPTY_COPY}</p>
    </div>
  )
}

function Suggestions({
  items,
  onPick,
}: {
  items: string[]
  onPick: (text: string) => void
}) {
  return (
    <div className="grid gap-2">
      <p className="text-[12px] font-medium text-muted-foreground">
        Try asking
      </p>
      <ul className="flex flex-wrap gap-1.5">
        {items.map((q) => (
          <li key={q}>
            <button
              type="button"
              onClick={() => onPick(q)}
              className="group/s flex items-center gap-1 rounded-full border border-dashed bg-background/70 px-3 py-1 text-left text-[12.5px] text-foreground transition-colors hover:border-solid hover:bg-muted"
            >
              {q}
              <CaretRightIcon className="size-3 text-muted-foreground transition-transform group-hover/s:translate-x-0.5" />
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}

function Working({ status }: { status?: string }) {
  const text =
    status === "waiting"
      ? "Waiting for an answer"
      : status === "queued"
        ? "Queued"
        : "Working"
  return (
    <p className="flex items-center gap-2 text-[13px] text-muted-foreground">
      <AgentOrb
        size={16}
        state={status === "waiting" ? "waiting" : "thinking"}
      />
      <span className={cn(status !== "waiting" && "agent-shimmer")}>
        {text}
      </span>
    </p>
  )
}

/** A run of the agent's parts: one part, or tool steps taken back to back. */
type Segment =
  | { kind: "part"; part: AgentPart; index: number }
  | { kind: "tools"; parts: DynamicToolUIPart[] }

/** segments folds tool steps taken back to back into one run, so a turn
 * that reads ten things shows them as one line until you open it. */
function segments(parts: AgentPart[]): Segment[] {
  const out: Segment[] = []
  parts.forEach((part, index) => {
    if (part.type === "dynamic-tool") {
      const last = out.at(-1)
      if (last?.kind === "tools") last.parts.push(part as DynamicToolUIPart)
      else out.push({ kind: "tools", parts: [part as DynamicToolUIPart] })
      return
    }
    out.push({ kind: "part", part, index })
  })
  return out
}

function AgentTurn({
  message,
  live,
}: {
  message: AgentMessage
  live: boolean
}) {
  const parts = message.parts.filter((p) => p.type !== "step-start")
  const status = message.metadata?.status
  const error = message.metadata?.error
  const explained = parts.some(
    (p) => p.type === "data-error" || p.type === "data-blocked"
  )
  const segs = segments(parts)
  const answer = textOf(message)
  return (
    <Message from="assistant">
      <MessageContent>
        {segs.map((seg, i) => {
          if (seg.kind === "tools")
            return seg.parts.length === 1 ? (
              <ToolStep
                key={seg.parts[0].toolCallId}
                part={seg.parts[0]}
                live={live}
              />
            ) : (
              <ToolRun
                key={seg.parts[0].toolCallId}
                parts={seg.parts}
                live={live}
                last={i === segs.length - 1}
              />
            )
          const p = seg.part
          const key = seg.index
          switch (p.type) {
            case "text":
              return <AgentMarkdown key={key} text={p.text} live={live} />
            case "data-request":
              return (
                <InlineRequest
                  key={p.id ?? key}
                  requestId={p.data.requestId}
                  live={live}
                />
              )
            case "data-blocked":
              return (
                <p
                  key={p.id ?? key}
                  className="rounded-md bg-muted/60 px-3 py-2 text-[13px] text-muted-foreground"
                >
                  {p.data.message}
                </p>
              )
            case "data-error":
              return (
                <p
                  key={p.id ?? key}
                  role="alert"
                  className="text-[13px] text-destructive"
                >
                  {p.data.message}
                </p>
              )
            default:
              return null
          }
        })}
        {live && (
          // A task that has written something is under way, whatever its
          // last status event said.
          <Working
            status={status === "queued" && parts.length ? "running" : status}
          />
        )}
        {!live && status === "failed" && !explained && (
          <p role="alert" className="text-[13px] text-destructive">
            {error || "The agent couldn't finish this answer."}
          </p>
        )}
        {!live && status === "cancelled" && (
          <p className="text-[12.5px] text-muted-foreground">Stopped.</p>
        )}
      </MessageContent>
      {!live && answer && (
        <MessageActions className="text-muted-foreground">
          <CopyButton value={answer} label="Copy answer" />
        </MessageActions>
      )}
    </Message>
  )
}

/** runLabel says what a run of tool steps did, as the steps would one by
 * one: "Listed deployments, read logs and 2 more". While one runs, it says
 * that one. */
function runLabel(parts: DynamicToolUIPart[], live: boolean): string {
  const running = live && parts.find((p) => p.state === "input-available")
  if (running) return toolLabel(running.toolName, true)
  const labels = [...new Set(parts.map((p) => toolLabel(p.toolName, false)))]
  const head = labels
    .slice(0, 2)
    .map((l, i) => (i ? l[0].toLowerCase() + l.slice(1) : l))
    .join(", ")
  return labels.length > 2 ? `${head} and ${labels.length - 2} more` : head
}

/** ToolRun is tool steps taken back to back, folded into one quiet line
 * that says what they did and what went wrong. It's open while the agent
 * is on it, and folds once the answer moves on. */
function ToolRun({
  parts,
  live,
  last,
}: {
  parts: DynamicToolUIPart[]
  live: boolean
  /** Whether nothing came after these steps yet. */
  last: boolean
}) {
  const [manual, setManual] = useState<boolean>()
  const open = manual ?? (live && last)
  const failed = parts.filter(
    (p) => p.state === "output-error" || toolMeta(p).outcome === "failed"
  ).length
  const refused = parts.filter((p) => toolMeta(p).outcome === "denied").length
  const running = live && parts.some((p) => p.state === "input-available")
  return (
    <Collapsible open={open} onOpenChange={setManual} className="group/run">
      <CollapsibleTrigger className="flex max-w-full items-center gap-1.5 rounded-sm text-left text-[13px] text-muted-foreground transition-colors outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50">
        <span className={cn("truncate", running && "agent-shimmer")}>
          {runLabel(parts, live)}
        </span>
        <span className="shrink-0 font-mono text-[11px] tabular-nums">
          {parts.length} steps
        </span>
        {failed > 0 && (
          <span className="shrink-0 text-[12px] text-destructive">
            {failed} failed
          </span>
        )}
        {refused > 0 && (
          <span className="shrink-0 text-[12px] text-weld-ink">
            {refused} refused
          </span>
        )}
        <CaretRightIcon
          weight="fill"
          className="size-2.5 shrink-0 transition-transform group-data-open/run:rotate-90"
        />
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-1.5 ml-1 grid gap-1.5 border-l-2 pl-3">
        {parts.map((p) => (
          <ToolStep key={p.toolCallId} part={p} live={live} />
        ))}
      </CollapsibleContent>
    </Collapsible>
  )
}

// How often a question the agent is waiting on is read again while its
// turn is live. agent_request.updated invalidates nothing yet, so this is
// how an answer given elsewhere shows up here.
const requestPoll = 5_000

function InlineRequest({
  requestId,
  live,
}: {
  requestId: string
  live: boolean
}) {
  const request = useQuery(useAgentRequestsGet, { path: { id: requestId } })
  const pending = request.data?.status === "pending"
  const { refetch } = request
  useEffect(() => {
    if (!live || !pending) return
    const id = window.setInterval(() => {
      refetch().catch(() => undefined)
    }, requestPoll)
    return () => window.clearInterval(id)
  }, [live, pending, refetch])
  if (!request.data) {
    if (request.status === "error")
      return (
        <p className="text-[13px] text-muted-foreground">
          The agent asked for something this page can&apos;t show. Open Agent
          requests to see it.
        </p>
      )
    return <Skeleton className="h-24 w-full rounded-lg" />
  }
  return <AgentRequestCard request={request.data} compact />
}
