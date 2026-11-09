// AI Elements' prompt input (ai-elements 1.9.1, registry "prompt-input"),
// cut down to the parts the Assistant uses: the form, its textarea, footer,
// tools and submit button. Attachments, screenshots, referenced sources, the
// action menu, model select, hover cards, tabs and the command list are left
// out, and with them nanoid and the hover-card part. The textarea keeps the
// original's keys: Enter sends, Shift+Enter is a new line, and Enter while
// an IME is composing does nothing. The submit button is a round arrow, and
// the box a raised card.
"use client"

import type { ChatStatus } from "ai"
import { ArrowUpIcon, SquareIcon } from "lucide-react"
import type {
  ComponentProps,
  FormEvent,
  FormEventHandler,
  HTMLAttributes,
  KeyboardEventHandler,
} from "react"
import { useCallback, useState } from "react"

import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupTextarea,
} from "@/components/ui/input-group"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"

export interface PromptInputMessage {
  text: string
}

export type PromptInputProps = Omit<
  HTMLAttributes<HTMLFormElement>,
  "onSubmit"
> & {
  onSubmit: (
    message: PromptInputMessage,
    event: FormEvent<HTMLFormElement>
  ) => void | Promise<void>
}

export const PromptInput = ({
  className,
  onSubmit,
  children,
  ...props
}: PromptInputProps) => {
  const handleSubmit: FormEventHandler<HTMLFormElement> = useCallback(
    (event) => {
      event.preventDefault()
      const data = new FormData(event.currentTarget)
      const text = (data.get("message") as string | null) ?? ""
      void onSubmit({ text }, event)
    },
    [onSubmit]
  )

  return (
    <form
      className={cn("w-full", className)}
      onSubmit={handleSubmit}
      {...props}
    >
      <InputGroup className="overflow-hidden rounded-xl bg-card shadow-xs has-[[data-slot=input-group-control]:focus-visible]:border-ring/60 has-[[data-slot=input-group-control]:focus-visible]:shadow-sm has-[[data-slot=input-group-control]:focus-visible]:ring-ring/15 dark:bg-input/30">
        {children}
      </InputGroup>
    </form>
  )
}

export type PromptInputBodyProps = HTMLAttributes<HTMLDivElement>

export const PromptInputBody = ({
  className,
  ...props
}: PromptInputBodyProps) => (
  <div className={cn("contents", className)} {...props} />
)

export type PromptInputTextareaProps = ComponentProps<typeof InputGroupTextarea>

export const PromptInputTextarea = ({
  onKeyDown,
  className,
  placeholder = "What would you like to know?",
  ...props
}: PromptInputTextareaProps) => {
  const [isComposing, setIsComposing] = useState(false)

  const handleKeyDown: KeyboardEventHandler<HTMLTextAreaElement> = useCallback(
    (e) => {
      onKeyDown?.(e)
      if (e.defaultPrevented) {
        return
      }
      if (e.key !== "Enter" || e.shiftKey) {
        return
      }
      if (isComposing || e.nativeEvent.isComposing) {
        return
      }
      e.preventDefault()
      // Enter does what the submit button would: nothing while it's off.
      const { form } = e.currentTarget
      const submitButton = form?.querySelector<HTMLButtonElement>(
        'button[type="submit"]'
      )
      if (submitButton?.disabled) {
        return
      }
      form?.requestSubmit()
    },
    [onKeyDown, isComposing]
  )

  return (
    <InputGroupTextarea
      className={cn("field-sizing-content max-h-48 min-h-16", className)}
      name="message"
      onCompositionEnd={() => setIsComposing(false)}
      onCompositionStart={() => setIsComposing(true)}
      onKeyDown={handleKeyDown}
      placeholder={placeholder}
      {...props}
    />
  )
}

export type PromptInputFooterProps = Omit<
  ComponentProps<typeof InputGroupAddon>,
  "align"
>

export const PromptInputFooter = ({
  className,
  ...props
}: PromptInputFooterProps) => (
  <InputGroupAddon
    align="block-end"
    className={cn("justify-between gap-1", className)}
    {...props}
  />
)

export type PromptInputToolsProps = HTMLAttributes<HTMLDivElement>

export const PromptInputTools = ({
  className,
  ...props
}: PromptInputToolsProps) => (
  <div
    className={cn("flex min-w-0 items-center gap-1", className)}
    {...props}
  />
)

export type PromptInputSubmitProps = ComponentProps<typeof InputGroupButton> & {
  status?: ChatStatus
  onStop?: () => void
}

export const PromptInputSubmit = ({
  className,
  variant = "default",
  size = "icon-sm",
  status,
  onStop,
  onClick,
  children,
  ...props
}: PromptInputSubmitProps) => {
  const isGenerating = status === "submitted" || status === "streaming"

  let Icon = <ArrowUpIcon className="size-4" />
  if (status === "submitted") {
    Icon = <Spinner />
  } else if (status === "streaming") {
    Icon = <SquareIcon className="size-4" />
  }

  const handleClick = useCallback(
    (e: Parameters<NonNullable<PromptInputSubmitProps["onClick"]>>[0]) => {
      if (isGenerating && onStop) {
        e.preventDefault()
        onStop()
        return
      }
      onClick?.(e)
    },
    [isGenerating, onStop, onClick]
  )

  return (
    <InputGroupButton
      aria-label={isGenerating ? "Stop" : "Send"}
      className={cn("rounded-full", className)}
      onClick={handleClick}
      size={size}
      type={isGenerating && onStop ? "button" : "submit"}
      variant={variant}
      {...props}
    >
      {children ?? Icon}
    </InputGroupButton>
  )
}
