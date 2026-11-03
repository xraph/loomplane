// One conversation with the agent: the person's messages, the agent's
// answers as they stream (text, tool steps, the questions it asks), and the
// box to write the next message in. The Assistant page and the slide-over
// panel both show it.
import { useChat } from "@ai-sdk/react"
import type { DynamicToolUIPart } from "ai"
import { SparklesIcon } from "lucide-react"
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
import type { History, Sent } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { AgentRequestCard } from "@/components/agent-request-card"
import {
  Conversation,
  ConversationContent,
  ConversationEmptyState,
  ConversationScrollButton,
} from "@/components/ai-elements/conversation"
import { Message, MessageContent } from "@/components/ai-elements/message"
import { Composer } from "@/components/assistant/composer"
import { AgentMarkdown } from "@/components/assistant/markdown"
import { ToolStep } from "@/components/assistant/tool-step"
import { QueryError } from "@/components/query-error"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  type AgentMessage,
  fromApi,
  historyToMessages,
} from "@/lib/agent-events"
import { AgentChatTransport, type OpenTurn } from "@/lib/agent-stream"
import { cn } from "@/lib/utils"

const terminal = new Set(["completed", "failed", "cancelled"])

export const EMPTY_COPY =
  "Ask about your projects, deployments and releases. The agent reads live state with its tools and asks before anything risky."

export type ConversationViewProps = {
  /** The conversation to show; none starts a new one. */
  conversationId?: string
  /** The project a new conversation is about; none is the whole workspace. */
  projectId?: string
  /** A tighter layout, for the slide-over panel. */
  compact?: boolean
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
  const [task, setTask] = useState(() => {
    const open = openTurnOf(history)
    return open ? { id: open.taskId, status: open.status } : undefined
  })

  const [transport] = useState(() => {
    const t = new AgentChatTransport({ conversationId, projectId })
    t.resumeFrom(openTurnOf(history))
    return t
  })

  // What the transport calls back into, kept current for it.
  useEffect(() => {
    transport.handle({
      start: async (body) => (await start.mutateAsync({ body })) as Sent,
      send: async (id, body) =>
        (await send.mutateAsync({ path: { id }, body })) as Sent,
      onSent: (sent) => {
        setTask({ id: sent.task.id, status: sent.task.status })
        invalidate(useAgentConversationsList)
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
    })
  })

  useEffect(() => {
    transport.setProject(projectId)
  }, [transport, projectId])

  const [initial] = useState(() => (history ? historyToMessages(history) : []))
  const [chatId] = useState(() => conversationId)
  const { messages, sendMessage, status, error, setMessages } =
    useChat<AgentMessage>({
      id: chatId,
      messages: initial,
      transport,
      resume: true,
      onError: () => {
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

  const submit = (text: string) => {
    setDraft("")
    setStopError(undefined)
    void sendMessage({ text })
  }

  const stop = async () => {
    if (!task) return
    setStopError(undefined)
    try {
      await cancel.mutateAsync({ path: { id: task.id } })
    } catch (err) {
      setStopError(messageOf(err, "The agent couldn't be stopped."))
    }
  }

  const lastAgent = messages.findLast((m) => m.role === "assistant")
  return (
    <div className={cn("flex min-h-0 flex-1 flex-col", className)}>
      <Conversation className="min-h-0">
        <ConversationContent
          className={cn(compact ? "gap-5 p-3" : "gap-6 px-4 py-5")}
        >
          {messages.length === 0 ? (
            <ConversationEmptyState
              icon={<SparklesIcon className="size-5" />}
              title="Ask the agent"
              description={EMPTY_COPY}
              className="min-h-[240px]"
            />
          ) : (
            messages.map((m) =>
              m.role === "user" ? (
                <Message key={m.id} from="user">
                  <MessageContent className="whitespace-pre-wrap">
                    {m.parts.map((p) => (p.type === "text" ? p.text : ""))}
                  </MessageContent>
                </Message>
              ) : (
                <AgentTurn
                  key={m.id}
                  message={m}
                  live={busy && m === lastAgent}
                />
              )
            )
          )}
          {streaming && messages.at(-1)?.role === "user" && (
            <Working status="queued" />
          )}
        </ConversationContent>
        <ConversationScrollButton />
      </Conversation>
      <Composer
        className={cn("shrink-0", compact ? "px-3 pb-3" : "px-4 pb-4")}
        value={draft}
        onChange={setDraft}
        onSend={submit}
        busy={busy}
        status={status}
        onStop={() => void stop()}
        stopping={cancel.isPending}
        error={stopError ?? error?.message}
        hint={hint}
      />
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
    <p className="flex items-center gap-2 text-[12.5px] text-muted-foreground">
      {status === "waiting" ? null : <Spinner className="size-3.5" />}
      {text}
    </p>
  )
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
  return (
    <Message from="assistant">
      <MessageContent>
        {parts.map((p, i) => {
          switch (p.type) {
            case "text":
              return <AgentMarkdown key={i} text={p.text} />
            case "dynamic-tool":
              return (
                <ToolStep
                  key={p.toolCallId}
                  part={p as DynamicToolUIPart}
                  live={live}
                />
              )
            case "data-request":
              return (
                <InlineRequest
                  key={p.id ?? i}
                  requestId={p.data.requestId}
                  live={live}
                />
              )
            case "data-blocked":
              return (
                <p
                  key={p.id ?? i}
                  className="rounded-md bg-muted/60 px-3 py-2 text-[13px] text-muted-foreground"
                >
                  {p.data.message}
                </p>
              )
            case "data-error":
              return (
                <p
                  key={p.id ?? i}
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
    </Message>
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
