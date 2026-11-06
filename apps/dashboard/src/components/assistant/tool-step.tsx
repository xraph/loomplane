// One of the agent's tool calls in a conversation, kept quiet: a muted
// line saying what it did ("Listed deployments"), shimmering while it
// runs, with its risk only when it's more than a read and a word when it
// went wrong. Opening it shows the tool, its risk, the arguments the
// server kept and the result.
import { CaretRightIcon } from "@phosphor-icons/react"
import type { DynamicToolUIPart } from "ai"

import { ToolInput, ToolOutput } from "@/components/ai-elements/tool"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { toolMeta } from "@/lib/agent-events"
import { riskLabel, riskTone, riskToneClass } from "@/lib/risk"
import { cn } from "@/lib/utils"

// The first word of a tool's name, said as done and as doing.
const verbs: Record<string, [done: string, doing: string]> = {
  list: ["Listed", "Listing"],
  get: ["Looked up", "Looking up"],
  read: ["Read", "Reading"],
  search: ["Searched", "Searching"],
  find: ["Found", "Finding"],
  check: ["Checked", "Checking"],
  describe: ["Described", "Describing"],
  show: ["Looked at", "Looking at"],
  whoami: ["Checked who you are", "Checking who you are"],
  create: ["Created", "Creating"],
  add: ["Added", "Adding"],
  update: ["Updated", "Updating"],
  set: ["Set", "Setting"],
  save: ["Saved", "Saving"],
  delete: ["Deleted", "Deleting"],
  drop: ["Dropped", "Dropping"],
  remove: ["Removed", "Removing"],
  approve: ["Approved", "Approving"],
  reject: ["Rejected", "Rejecting"],
  plan: ["Planned", "Planning"],
  deploy: ["Deployed", "Deploying"],
  promote: ["Promoted", "Promoting"],
  rollback: ["Rolled back", "Rolling back"],
  cancel: ["Cancelled", "Cancelling"],
  prepare: ["Prepared", "Preparing"],
  request: ["Asked for", "Asking for"],
  ask: ["Asked", "Asking"],
}

// Tools whose names don't start with a verb, said whole.
const named: Record<string, [done: string, doing: string]> = {
  deployed: ["Checked what's deployed", "Checking what's deployed"],
  get_repository_state: [
    "Checked the repositories",
    "Checking the repositories",
  ],
  status: ["Checked status", "Checking status"],
  drift: ["Checked for drift", "Checking for drift"],
  analyze: ["Analyzed", "Analyzing"],
  github_status: ["Checked GitHub", "Checking GitHub"],
  knowledge_search: ["Searched the docs", "Searching the docs"],
  binding_metrics: ["Read metrics", "Reading metrics"],
  canary_report: ["Read the canary report", "Reading the canary report"],
  recent_canaries: ["Looked at recent canaries", "Looking at recent canaries"],
  rollback_target: ["Found a rollback target", "Finding a rollback target"],
  deployment_for_plan: ["Found the deployment", "Finding the deployment"],
  deployment_logs: ["Read deployment logs", "Reading deployment logs"],
  open_report: ["Opened a report", "Opening a report"],
  note_release_concern: ["Noted a release concern", "Noting a release concern"],
}

/** toolLabel says what a tool call did, or is doing, in a few words:
 * list_deployments is "Listed deployments", and while it runs "Listing
 * deployments". A name it can't read is said as "Ran <name>". */
export function toolLabel(name: string, running: boolean): string {
  const whole = named[name]
  if (whole) return whole[running ? 1 : 0]
  const [first, ...rest] = name.split(/[_.\-\s]+/).filter(Boolean)
  const verb = first ? verbs[first.toLowerCase()] : undefined
  if (!verb) return running ? `Running ${name}` : `Ran ${name}`
  const said = verb[running ? 1 : 0]
  return rest.length ? `${said} ${rest.join(" ")}` : said
}

export function ToolStep({
  part,
  live = true,
}: {
  part: DynamicToolUIPart
  /** Whether the turn is still running; a call of a finished turn with no
   * result isn't running any more. */
  live?: boolean
}) {
  const meta = toolMeta(part)
  const running = live && part.state === "input-available"
  const unsettled = part.state === "input-available" && !live
  const failed = part.state === "output-error" || meta.outcome === "failed"
  const refused = meta.outcome === "denied"
  const quietRisk = meta.risk === "read"
  return (
    <Collapsible className="group/step">
      <CollapsibleTrigger
        className="flex max-w-full items-center gap-1.5 rounded-sm text-left text-[13px] text-muted-foreground transition-colors outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
        title={part.toolName}
      >
        <span className={cn("truncate", running && "agent-shimmer")}>
          {toolLabel(part.toolName, running)}
        </span>
        {!quietRisk && (
          <span
            className={cn(
              "shrink-0 rounded-full px-1.5 text-[11px] leading-[18px] ring-1 ring-current/20",
              riskToneClass[riskTone(meta.risk)]
            )}
          >
            {riskLabel(meta.risk)}
          </span>
        )}
        {failed && (
          <span className="shrink-0 text-[12px] text-destructive">failed</span>
        )}
        {refused && (
          <span className="shrink-0 text-[12px] text-weld-ink">refused</span>
        )}
        {unsettled && <span className="shrink-0 text-[12px]">no result</span>}
        <CaretRightIcon
          weight="fill"
          className="size-2.5 shrink-0 transition-transform group-data-open/step:rotate-90"
        />
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-1.5 ml-1 overflow-hidden rounded-md border-l-2 bg-subtle/70 text-[12.5px]">
        <p className="flex flex-wrap items-center gap-x-2 px-3 pt-2 text-muted-foreground">
          <span className="font-mono text-foreground">{part.toolName}</span>
          <span>{riskLabel(meta.risk)}</span>
        </p>
        <ToolInput
          className="px-3 pt-2"
          input={meta.stored ? part.input : undefined}
          empty="Arguments not stored. Steps that need a person, or touch a secret, keep none."
        />
        <ToolOutput
          className="px-3 pt-2"
          output={part.output}
          errorText={part.errorText}
        />
        <div className="h-3" />
      </CollapsibleContent>
    </Collapsible>
  )
}
