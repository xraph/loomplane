// AI Elements' tool (ai-elements 1.9.1, registry "tool"), patched for the
// dashboard:
//   - Base UI's Collapsible marks itself data-open, not data-state=open, so
//     the chevron and the panel's animation follow those attributes.
//   - ToolInput and ToolOutput show JSON in a plain <pre> instead of the
//     CodeBlock part: a step's arguments are a few lines, and Shiki's
//     grammars and engine aren't worth their weight for them.
//   - ToolHeader takes children, shown after the status, for the step's
//     risk badge, and a statusLabel to say instead of the state's own.
//   - The status labels are Loomplane's words, and the header's wrapper is a
//     span: a button holds phrasing content only.
"use client"

import type { DynamicToolUIPart, ToolUIPart } from "ai"
import {
  CheckCircleIcon,
  ChevronDownIcon,
  CircleIcon,
  ClockIcon,
  WrenchIcon,
  XCircleIcon,
} from "lucide-react"
import type { ComponentProps, ReactNode } from "react"
import { isValidElement } from "react"

import { Badge } from "@/components/ui/badge"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { cn } from "@/lib/utils"

export type ToolProps = ComponentProps<typeof Collapsible>

export const Tool = ({ className, ...props }: ToolProps) => (
  <Collapsible
    className={cn(
      "group not-prose w-full rounded-lg border bg-card text-[13px]",
      className
    )}
    {...props}
  />
)

export type ToolPart = ToolUIPart | DynamicToolUIPart

export type ToolHeaderProps = {
  title?: string
  className?: string
  children?: ReactNode
  /** Says this instead of the state's own label. */
  statusLabel?: string
} & (
  | { type: ToolUIPart["type"]; state: ToolUIPart["state"]; toolName?: never }
  | {
      type: DynamicToolUIPart["type"]
      state: DynamicToolUIPart["state"]
      toolName: string
    }
)

const statusLabels: Record<ToolPart["state"], string> = {
  "approval-requested": "Waiting for approval",
  "approval-responded": "Answered",
  "input-available": "Running",
  "input-streaming": "Starting",
  "output-available": "Done",
  "output-denied": "Refused",
  "output-error": "Failed",
}

const statusIcons: Record<ToolPart["state"], ReactNode> = {
  "approval-requested": <ClockIcon className="size-4 text-weld-ink" />,
  "approval-responded": <CheckCircleIcon className="size-4 text-flight-ink" />,
  "input-available": <ClockIcon className="size-4 animate-pulse" />,
  "input-streaming": <CircleIcon className="size-4" />,
  "output-available": <CheckCircleIcon className="size-4 text-overdye-ink" />,
  "output-denied": <XCircleIcon className="size-4 text-weld-ink" />,
  "output-error": <XCircleIcon className="size-4 text-destructive" />,
}

export const getStatusBadge = (status: ToolPart["state"], label?: string) => (
  <Badge className="gap-1.5 rounded-full text-xs" variant="secondary">
    {label ? <CircleIcon className="size-4" /> : statusIcons[status]}
    {label ?? statusLabels[status]}
  </Badge>
)

export const ToolHeader = ({
  className,
  title,
  type,
  state,
  toolName,
  children,
  statusLabel,
  ...props
}: ToolHeaderProps) => {
  const derivedName =
    type === "dynamic-tool" ? toolName : type.split("-").slice(1).join("-")

  return (
    <CollapsibleTrigger
      className={cn(
        "flex w-full items-center justify-between gap-4 px-3 py-2 text-left",
        className
      )}
      {...props}
    >
      <span className="flex min-w-0 flex-wrap items-center gap-2">
        <WrenchIcon className="size-4 shrink-0 text-muted-foreground" />
        <span className="truncate font-mono text-[13px] font-medium">
          {title ?? derivedName}
        </span>
        {getStatusBadge(state, statusLabel)}
        {children}
      </span>
      <ChevronDownIcon className="size-4 shrink-0 text-muted-foreground transition-transform group-data-open:rotate-180" />
    </CollapsibleTrigger>
  )
}

export type ToolContentProps = ComponentProps<typeof CollapsibleContent>

export const ToolContent = ({ className, ...props }: ToolContentProps) => (
  <CollapsibleContent
    className={cn(
      "space-y-4 p-4 pt-0 text-popover-foreground outline-none data-open:animate-in data-open:fade-in-0 data-closed:animate-out data-closed:fade-out-0",
      className
    )}
    {...props}
  />
)

const json = (value: unknown) =>
  typeof value === "string" ? value : JSON.stringify(value, null, 2)

const Code = ({ children }: { children: string }) => (
  <pre className="max-w-full overflow-x-auto p-3 font-mono text-[12px] leading-relaxed whitespace-pre-wrap">
    {children}
  </pre>
)

export type ToolInputProps = ComponentProps<"div"> & {
  input: ToolPart["input"]
  /** Shown instead of the input when the step kept none. */
  empty?: ReactNode
}

export const ToolInput = ({
  className,
  input,
  empty,
  ...props
}: ToolInputProps) => (
  <div className={cn("space-y-2 overflow-hidden", className)} {...props}>
    <h4 className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
      Arguments
    </h4>
    <div className="rounded-md bg-muted/50">
      {input === undefined && empty ? (
        <p className="p-3 text-[12.5px] text-muted-foreground">{empty}</p>
      ) : (
        <Code>{json(input)}</Code>
      )}
    </div>
  </div>
)

export type ToolOutputProps = ComponentProps<"div"> & {
  output: ToolPart["output"]
  errorText: ToolPart["errorText"]
}

export const ToolOutput = ({
  className,
  output,
  errorText,
  ...props
}: ToolOutputProps) => {
  if (!(output || errorText)) {
    return null
  }

  let Output = <div>{output as ReactNode}</div>
  if (typeof output === "object" && !isValidElement(output)) {
    Output = <Code>{json(output)}</Code>
  } else if (typeof output === "string") {
    Output = <p className="p-3 text-[12.5px]">{output}</p>
  }

  return (
    <div className={cn("space-y-2", className)} {...props}>
      <h4 className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
        {errorText ? "Error" : "Result"}
      </h4>
      <div
        className={cn(
          "overflow-x-auto rounded-md text-xs [&_table]:w-full",
          errorText
            ? "bg-destructive/10 text-destructive"
            : "bg-muted/50 text-foreground"
        )}
      >
        {errorText && <p className="p-3 text-[12.5px]">{errorText}</p>}
        {!errorText && Output}
      </div>
    </div>
  )
}
