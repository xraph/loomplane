// Where a person writes to the agent. Enter sends and Shift+Enter starts a
// new line; Up in an empty box brings back the last message sent. While
// the agent is on a message the box is off and its button stops the agent
// instead; why the server refused a message shows below.
import type { ChatStatus } from "ai"
import { useEffect, useRef } from "react"

import {
  PromptInput,
  PromptInputBody,
  PromptInputFooter,
  PromptInputSubmit,
  PromptInputTextarea,
  PromptInputTools,
} from "@/components/ai-elements/prompt-input"
import { InputGroupAddon } from "@/components/ui/input-group"
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
  scope,
  lastSent,
  autoFocus,
  placeholder = "Ask the agent",
  onFocusChange,
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
  /** Under the box on the left, such as a word on what Enter does. */
  hint?: React.ReactNode
  /** Above the text, what the message is about: the conversation's scope
   * and the page it's sent from. */
  scope?: React.ReactNode
  /** The person's last message, for Up to bring back. */
  lastSent?: string
  /** Focus the box when it appears, and again when the agent finishes. */
  autoFocus?: boolean
  placeholder?: string
  /** onFocusChange hears the box gaining and losing focus. */
  onFocusChange?: (focused: boolean) => void
  className?: string
}) {
  const text = value.trim()
  const box = useRef<HTMLTextAreaElement>(null)
  const near = value.length > MAX_MESSAGE * 0.9

  useEffect(() => {
    if (autoFocus && !busy) box.current?.focus({ preventScroll: true })
  }, [autoFocus, busy])

  return (
    <div className={cn("grid gap-1.5", className)}>
      <PromptInput
        onSubmit={() => {
          if (text && !busy) onSend(text)
        }}
      >
        {scope && (
          <InputGroupAddon
            align="block-start"
            className="flex-wrap justify-start gap-1 pb-0"
          >
            {scope}
          </InputGroupAddon>
        )}
        <PromptInputBody>
          <PromptInputTextarea
            ref={box}
            aria-label="Message the agent"
            placeholder={placeholder}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "ArrowUp" && !value && lastSent) {
                e.preventDefault()
                onChange(lastSent)
              }
            }}
            disabled={busy}
            maxLength={MAX_MESSAGE}
            onFocus={() => onFocusChange?.(true)}
            onBlur={() => onFocusChange?.(false)}
          />
        </PromptInputBody>
        <PromptInputFooter>
          <PromptInputTools className="text-[12px] text-muted-foreground">
            {hint}
          </PromptInputTools>
          <div className="flex items-center gap-2">
            {near && (
              <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
                {value.length.toLocaleString()} / {MAX_MESSAGE.toLocaleString()}
              </span>
            )}
            <PromptInputSubmit
              status={
                busy
                  ? status === "submitted"
                    ? status
                    : "streaming"
                  : undefined
              }
              onStop={onStop}
              disabled={busy ? stopping : !text}
            />
          </div>
        </PromptInputFooter>
      </PromptInput>
      {error && (
        <p role="alert" className="px-1 text-[12.5px] text-destructive">
          {error}
        </p>
      )}
    </div>
  )
}
