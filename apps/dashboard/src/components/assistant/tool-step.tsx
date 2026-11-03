// One of the agent's tool calls in a conversation: the tool, how risky it
// is and how it went, opening to the arguments the server kept.
import type { DynamicToolUIPart } from "ai"

import {
  Tool,
  ToolContent,
  ToolHeader,
  ToolInput,
  ToolOutput,
} from "@/components/ai-elements/tool"
import { Badge } from "@/components/ui/badge"
import { toolMeta } from "@/lib/agent-events"
import { riskLabel, riskTone, riskToneClass } from "@/lib/risk"
import { cn } from "@/lib/utils"

export function ToolStep({ part }: { part: DynamicToolUIPart }) {
  const meta = toolMeta(part)
  // A refused call is an error to the AI SDK; the header says it was refused.
  const state = meta.outcome === "denied" ? "output-denied" : part.state
  return (
    <Tool>
      <ToolHeader type="dynamic-tool" toolName={part.toolName} state={state}>
        <Badge
          variant="outline"
          className={cn("rounded-full", riskToneClass[riskTone(meta.risk)])}
        >
          {riskLabel(meta.risk)}
        </Badge>
      </ToolHeader>
      <ToolContent>
        <ToolInput
          input={meta.stored ? part.input : undefined}
          empty="Arguments not stored. Steps that need a person, or touch a secret, keep none."
        />
        <ToolOutput output={part.output} errorText={part.errorText} />
      </ToolContent>
    </Tool>
  )
}
