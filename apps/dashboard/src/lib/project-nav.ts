import type { Environment } from "@/client/forge/loomplane/types"

/**
 * A project's views, in the order its nav lists them. Overview is the
 * default. Everything about one service, deployment or resource opens on
 * a page of its own under the project, with its tab still marked.
 */
export const projectTabs = [
  "overview",
  "services",
  "deployments",
  "variables",
  "settings",
] as const
export type ProjectTab = (typeof projectTabs)[number]

/**
 * The parts of a project's Settings, in the order its nav lists them.
 * General ends with the danger zone.
 */
export const settingsSections = [
  "general",
  "access",
  "source",
  "clouds",
  "manifest",
] as const
export type SettingsSection = (typeof settingsSections)[number]

export type ProjectSearch = { tab?: ProjectTab; section?: SettingsSection }

// Older links name tabs the project has since folded into others: the
// graph is on Overview, a project's Clouds and its analyses are part of
// Settings, and each database keeps its own backups on its page, which
// the Services tab lists.
const moved: Record<string, ProjectSearch> = {
  graph: { tab: "overview" },
  deploy: { tab: "deployments" },
  cloud: { tab: "settings", section: "clouds" },
  analyses: { tab: "settings", section: "source" },
  backups: { tab: "services" },
}

/**
 * projectSearchOf reads ?tab= and ?section= from a project's address. An
 * old tab opens where its content went; an unknown one is dropped, so the
 * page opens on Overview. A section only means something on Settings.
 */
export function projectSearchOf(
  search: Record<string, unknown>
): ProjectSearch {
  const raw = typeof search.tab === "string" ? search.tab : ""
  const out: ProjectSearch = (projectTabs as readonly string[]).includes(raw)
    ? { tab: raw as ProjectTab }
    : { ...moved[raw] }
  if (out.tab === "overview") delete out.tab
  const section = typeof search.section === "string" ? search.section : ""
  if (
    out.tab === "settings" &&
    !out.section &&
    (settingsSections as readonly string[]).includes(section)
  )
    out.section = section as SettingsSection
  return out
}

/** environmentSearchOf reads ?environment=, a name no longer than 64. */
export function environmentSearchOf(value: unknown): string | undefined {
  return typeof value === "string" && value.length > 0 && value.length <= 64
    ? value
    : undefined
}

/**
 * chosenEnvironment is the environment a project's views show: the one
 * named, else production, else the first. Undefined only when the project
 * has none.
 */
export function chosenEnvironment(
  environments: Environment[],
  name?: string
): Environment | undefined {
  return (
    (name ? environments.find((e) => e.name === name) : undefined) ??
    environments.find((e) => e.kind === "production") ??
    environments[0]
  )
}
