// What the person is looking at, read from the dashboard's address, for
// the agent: a message carries it, so "why is this failing?" on a
// service's page is about that service. The label is what the composer
// shows and what the agent reads; the path is where the agent links back
// to. The server checks it again (agentchat), so this only has to be
// right, not trusted.

export type PageKind =
  | "project"
  | "environment"
  | "service"
  | "fleet"
  | "resource"
  | "deployment"
  | "cloud"
  | "domain"
  | "node"
  | "report"
  | "page"

export type PageContext = {
  kind: PageKind
  /** The dashboard path, search included, without the workspace. */
  path: string
  /** What the page is, in words: "Service web in api, production". */
  label: string
  projectId?: string
  service?: string
  environment?: string
  planId?: string
}

// Pages with nothing more to them than their name.
const named: Record<string, string> = {
  "/": "Overview",
  "/deployments": "Deployments",
  "/projects": "Projects",
  "/services": "Services",
  "/services/catalog": "the service catalog",
  "/clouds": "Clouds",
  "/infrastructure": "Infrastructure",
  "/activity": "Activity",
  "/variables": "Variables",
  "/members": "Members",
  "/teams": "Teams",
  "/domains": "Domains",
  "/nodes": "Nodes",
  "/integrations": "Integrations",
  "/agent/requests": "the agent's requests",
  "/agent/reports": "the agent's reports",
  "/ai/models": "Models",
  "/notifications": "Notifications",
}

const MAX_LABEL = 120

/**
 * pageContextOf reads where someone is from a path and its search. names
 * gives project names by ID; a project it doesn't know yet is "this
 * project". Settings and account pages, and anything unknown, have no
 * context: they're not what a question is about.
 */
export function pageContextOf(
  path: string,
  search: Record<string, unknown>,
  names: ReadonlyMap<string, string>
): PageContext | undefined {
  const clean = path.replace(/\/+$/, "") || "/"
  const env =
    typeof search.environment === "string" && search.environment
      ? search.environment
      : undefined
  const query = env ? `?environment=${encodeURIComponent(env)}` : ""
  const seg = clean.split("/").filter(Boolean).map(decodeURIComponent)
  const projectName = (id: string) => names.get(id) ?? "this project"
  const cut = (label: string) =>
    label.length > MAX_LABEL ? `${label.slice(0, MAX_LABEL - 1)}…` : label
  const ctx = (c: Omit<PageContext, "label"> & { label: string }) => ({
    ...c,
    label: cut(c.label),
  })

  if (seg[0] === "projects" && seg[1]) {
    const projectId = seg[1]
    const project = projectName(projectId)
    const base = `/projects/${encodeURIComponent(projectId)}`
    if (seg[2] === "services" && seg[3])
      return ctx({
        kind: "service",
        path: `${base}/services/${encodeURIComponent(seg[3])}${query}`,
        label: `Service ${seg[3]} in ${project}${env ? `, ${env}` : ""}`,
        projectId,
        service: seg[3],
        ...(env ? { environment: env } : {}),
      })
    if (seg[2] === "fleet")
      return ctx({
        kind: "fleet",
        path: `${base}/fleet`,
        label: seg[4]
          ? `Fleet instance ${seg[4]} in ${project}`
          : `The fleet of ${project}`,
        projectId,
      })
    if ((seg[2] === "catalog" || seg[2] === "ai") && seg.at(-1)) {
      const resource = seg.at(-1) as string
      return ctx({
        kind: "resource",
        path: clean,
        label: `${seg[2] === "ai" ? "Model" : "Resource"} ${resource} in ${project}`,
        projectId,
      })
    }
    if (env)
      return ctx({
        kind: "environment",
        path: `${base}${query}`,
        label: `${project}, ${env}`,
        projectId,
        environment: env,
      })
    return ctx({
      kind: "project",
      path: base,
      label: `Project ${project}`,
      projectId,
    })
  }
  if (seg[0] === "deployments" && seg[1] && seg[1] !== "new")
    return ctx({
      kind: "deployment",
      path: `/deployments/${encodeURIComponent(seg[1])}`,
      label: `Deployment ${seg[1]}`,
      planId: seg[1],
    })
  if (seg[0] === "clouds" && seg.length > 1)
    return ctx({ kind: "cloud", path: clean, label: `Cloud ${seg.at(-1)}` })
  if (seg[0] === "domains" && seg[1])
    return ctx({ kind: "domain", path: clean, label: `Domain ${seg[1]}` })
  if (seg[0] === "nodes" && seg[1])
    return ctx({ kind: "node", path: clean, label: `Node ${seg[1]}` })
  if (
    (seg[0] === "release-reports" && seg[1]) ||
    (seg[0] === "agent" && seg[1] === "reports" && seg[2])
  )
    return ctx({ kind: "report", path: clean, label: `Report ${seg.at(-1)}` })
  const page = named[clean]
  return page ? ctx({ kind: "page", path: clean, label: page }) : undefined
}
