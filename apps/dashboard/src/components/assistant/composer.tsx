// Where a person writes to the agent. Enter sends and Shift+Enter starts a
// new line. While the agent is on a message the box is off and its button
// stops the agent instead; why the server refused a message shows below.
import type { ChatStatus } from "ai"

import {
  PromptInput,
  PromptInputBody,
  PromptInputFooter,
  PromptInputSubmit,
  PromptInputTextarea,
  PromptInputTools,
} from "@/components/ai-elements/prompt-input"
import { cn } from "@/lib/utils"

/** The most characters one message may have (agentchat.MaxMessage). */
export const MAX_MESSAGE = 16000

export function Composer({
  value,
  onChange,
  onSend,
  busy,
  status,
  onStop,
  stopping,
  error,
  hint,
  className,
}: {
  value: string
  onChange: (value: string) => void
  onSend: (text: string) => void
  /** The agent is on the last message: queued, running or waiting. */
  busy: boolean
  status: ChatStatus
  onStop: () => void
  stopping: boolean
  /** Why the last message didn't go, in the API's words, or what went
   * wrong with the answer. */
  error?: React.ReactNode
  /** A line under the box, such as what the conversation is about. */
  hint?: React.ReactNode
  className?: string
}) {
  const text = value.trim()
  return (
    <div className={cn("grid gap-1.5", className)}>
      <PromptInput
        onSubmit={() => {
          if (text && !busy) onSend(text)
        }}
      >
        <PromptInputBody>
          <PromptInputTextarea
            aria-label="Message the agent"
            placeholder="Ask the agent"
            value={value}
            onChange={(e) => onChange(e.target.value)}
            disabled={busy}
            maxLength={MAX_MESSAGE}
          />
        </PromptInputBody>
        <PromptInputFooter>
          <PromptInputTools className="text-[12px] text-muted-foreground">
            {hint}
          </PromptInputTools>
          <PromptInputSubmit
            status={
              busy ? (status === "submitted" ? status : "streaming") : undefined
            }
            onStop={onStop}
            disabled={busy ? stopping : !text}
          />
        </PromptInputFooter>
      </PromptInput>
      {error && (
        <p role="alert" className="text-[12.5px] text-destructive">
          {error}
        </p>
      )}
    </div>
  )
}
