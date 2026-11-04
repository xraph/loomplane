// How risky an agent step is, in the words and colors the dashboard shows.
// The classes are the agent's operation risks (pkg/agentops); a step the
// server didn't class reads as a read.

export type Risk =
  | "read"
  | "change"
  | "deploy_nonproduction"
  | "promote_production"
  | "destroy_production"
  | "secret_or_external"

export type RiskTone = "neutral" | "info" | "attention" | "danger"

const risks: Record<Risk, { label: string; tone: RiskTone }> = {
  read: { label: "Read", tone: "neutral" },
  change: { label: "Change", tone: "info" },
  deploy_nonproduction: { label: "Deploy, not production", tone: "info" },
  promote_production: { label: "Production release", tone: "attention" },
  destroy_production: { label: "Removes production data", tone: "danger" },
  secret_or_external: { label: "Needs a person", tone: "attention" },
}

/** riskOf reads a risk the server sent; anything else is a read. */
export function riskOf(value: unknown): Risk {
  return typeof value === "string" && Object.hasOwn(risks, value)
    ? (value as Risk)
    : "read"
}

export function riskLabel(r: Risk): string {
  return risks[riskOf(r)].label
}

export function riskTone(r: Risk): RiskTone {
  return risks[riskOf(r)].tone
}

/** The badge classes of each tone, on the status colors. */
export const riskToneClass: Record<RiskTone, string> = {
  neutral: "border-border bg-transparent text-muted-foreground",
  info: "bg-flight-tint text-flight-ink",
  attention: "bg-weld-tint text-weld-ink",
  danger: "bg-destructive/10 text-destructive dark:bg-destructive/20",
}
