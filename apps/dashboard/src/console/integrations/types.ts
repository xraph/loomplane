/**
 * The wire shapes of the backend's "integrations" contributor
 * (extensions/connections/console.go). Field names are its JSON tags.
 */

export type AppSource = "config" | "platform" | "none"

export type IntegrationKind = "cloud" | "repository"

export type Health =
  "sign_in_needed" | "app_changed" | "failed" | "incomplete" | "healthy"

/** Worst first, as the backend sorts them. */
export const HEALTH_ORDER: Health[] = [
  "sign_in_needed",
  "app_changed",
  "failed",
  "incomplete",
  "healthy",
]

export interface SignInCounts {
  total: number
  healthy: number
  incomplete: number
  failed: number
  signInNeeded: number
  appChanged: number
}

export interface ConsoleProvider {
  name: string
  consoleUrl?: string
  docsUrl?: string
  notes?: string[]
  scopes?: string[]
  /** What the app needs besides its client ID and secret, in form order. */
  fields?: ConsoleField[]
  /** What breaks when a repository host's app is replaced or removed. */
  changeWarning?: string
}

/** One more value an app needs. name is its field in the save payload. */
export interface ConsoleField {
  name: "slug" | "appId" | "privateKey" | "webhookSecret"
  label: string
  hint?: string
  /** "pem" is a secret pasted over several lines. */
  kind: "text" | "number" | "secret" | "pem"
  optional?: boolean
}

/** A secret field that can hold a saved value. */
export type SecretName = "clientSecret" | "privateKey" | "webhookSecret"

export interface ConsoleIntegration {
  key: string
  name: string
  kind: IntegrationKind
  /** The console can set its app: a cloud's, when config doesn't. */
  editable: boolean
  source: AppSource
  clientId?: string
  slug?: string
  /** The GitHub App's ID. */
  appId?: number
  /** The secret fields that hold a saved value; never the values. */
  savedSecrets?: SecretName[]
  updatedAt?: string
  updatedBy?: string
  /** Why the cloud last refused the app. */
  problem?: string
  callbackUrl: string
  /** Its connections keep working when the app changes (Vercel's). */
  outlivesApp?: boolean
  /** Absent for a repository host, or when health can't be read. */
  connections?: SignInCounts
  /** Organizations with a working sign-in through the current app. */
  organizations: number
  provider: ConsoleProvider
}

export interface IntegrationsResponse {
  /** The product app, where people sign a connection in again. */
  appUrl: string
  healthAvailable: boolean
  integrations: ConsoleIntegration[]
  totals: SignInCounts
}

export interface IntegrationDetail extends ConsoleIntegration {
  appUrl: string
  healthAvailable: boolean
  envVars: string[]
  configKey: string
}

export interface SignedInConnection {
  id: string
  orgId: string
  orgName?: string
  cloud: string
  name: string
  account?: string
  health: Health
  status: string
  message?: string
  signedInBy?: string
  signedInByEmail?: string
  checkedAt?: string
  createdAt: string
  updatedAt: string
}

export interface SignedInPage {
  items: SignedInConnection[]
  total: number
  limit: number
  offset: number
  counts: SignInCounts
}

export type CheckOutcome =
  "accepted" | "rejected" | "redirect_mismatch" | "unreachable" | "inconclusive"

export interface AppCheck {
  result: CheckOutcome
  summary: string
  detail?: string
  checkedAt: string
}

export interface AckResponse {
  key: string
}

/** Signed-in connections someone has to sign in again, or that fail. */
export function brokenCount(c: SignInCounts | undefined): number {
  return c ? c.signInNeeded + c.appChanged + c.failed : 0
}

/** How many connections have health h ("" for all, "problems" for broken). */
export function countOf(
  c: SignInCounts | undefined,
  h: Health | "" | "problems"
): number {
  if (!c) {
    return 0
  }
  switch (h) {
    case "":
      return c.total
    case "problems":
      return brokenCount(c)
    case "sign_in_needed":
      return c.signInNeeded
    case "app_changed":
      return c.appChanged
    default:
      return c[h]
  }
}

/**
 * Where an integration stands, in the order an operator works through them:
 * an app the cloud refused, then broken sign-ins, then what isn't set up.
 */
export type SetupState = "refused" | "broken" | "not_set_up" | "ready"

export const SETUP_ORDER: SetupState[] = [
  "refused",
  "broken",
  "not_set_up",
  "ready",
]

export function setupState(it: ConsoleIntegration): SetupState {
  if (it.problem) {
    return "refused"
  }
  if (brokenCount(it.connections) > 0) {
    return "broken"
  }
  return it.source === "none" ? "not_set_up" : "ready"
}

/**
 * Whether saving these values, or removing the app (next null), breaks the
 * connections made with the current one. A cloud's sign-ins belong to its
 * client ID, so a new one matters once anyone has signed in; Vercel's
 * outlive their app. A repository host's belong to what changeWarning
 * names: the GitHub App's ID, or GitLab's and Bitbucket's client ID.
 */
export function replacesApp(
  it: ConsoleIntegration,
  next: { clientId: string; appId?: string } | null
): boolean {
  if (it.outlivesApp || !it.clientId || it.source !== "platform") {
    return false
  }
  if (it.kind === "cloud" && it.organizations === 0) {
    return false
  }
  if (next === null) {
    return true
  }
  if (it.key === "github") {
    return (
      it.appId !== undefined && (next.appId ?? "").trim() !== String(it.appId)
    )
  }
  return next.clientId.trim() !== it.clientId
}

/** The values a form starts from: what's saved, and no secrets. */
export function initialValues(it: ConsoleIntegration): Record<string, string> {
  const v: Record<string, string> = {}
  for (const f of it.provider.fields ?? []) {
    if (f.name === "slug") v.slug = it.slug ?? ""
    else if (f.name === "appId") v.appId = it.appId ? String(it.appId) : ""
    else v[f.name] = ""
  }
  return v
}

/**
 * Whether a blank secret field keeps its saved value: it holds one, and
 * what it belongs to is unchanged (the client secret its client ID, the
 * GitHub App's key its App ID). The webhook secret is the App's own.
 */
export function keepsSecret(
  it: ConsoleIntegration,
  name: SecretName,
  values: { clientId: string; appId?: string }
): boolean {
  if (it.source !== "platform" || !it.savedSecrets?.includes(name)) {
    return false
  }
  switch (name) {
    case "clientSecret":
      return values.clientId.trim() === it.clientId
    case "privateKey":
      return (values.appId ?? "").trim() === String(it.appId ?? "")
    default:
      return true
  }
}

/**
 * Reads a pasted credentials file: Google's {"web": {client_id,
 * client_secret}} (or "installed"), or a flat {client_id, client_secret}.
 */
export function parseCredentials(
  text: string
): { clientId: string; clientSecret?: string } | null {
  const trimmed = text.trim()
  if (!trimmed.startsWith("{")) {
    return null
  }
  try {
    const raw = JSON.parse(trimmed) as Record<string, unknown>
    const inner = (raw.web ?? raw.installed ?? raw) as Record<string, unknown>
    const id = inner.client_id ?? inner.clientId
    const secret = inner.client_secret ?? inner.clientSecret
    if (typeof id !== "string" || id === "") {
      return null
    }
    return {
      clientId: id,
      clientSecret: typeof secret === "string" ? secret : undefined,
    }
  } catch {
    return null
  }
}

/**
 * A message for the people in the connection's organization, who are the only
 * ones who can sign it in again.
 */
export function signInAgainMessage(
  c: SignedInConnection,
  cloudName: string,
  appUrl: string
): string {
  const where = c.orgName || "your organization"
  const who = c.account ? ` (${c.account})` : ""
  const link = appUrl
    ? `${appUrl}/clouds/accounts/${encodeURIComponent(c.id)}`
    : "the connection's page in Loomplane"
  switch (c.health) {
    case "sign_in_needed":
    case "app_changed": {
      const why =
        c.health === "app_changed"
          ? "was signed in with an app Loomplane no longer uses"
          : `is no longer accepted by ${cloudName}`
      return `The ${cloudName} connection "${c.name}"${who} in ${where} ${why}, so nothing deploys through it. Open ${link} and choose Sign in again.`
    }
    case "failed":
      return `The ${cloudName} connection "${c.name}"${who} in ${where} failed its last check${c.message ? `: ${c.message}` : ""}. Open ${link} and choose Check again; if it keeps failing, Sign in again.`
    case "incomplete":
      return `The ${cloudName} connection "${c.name}"${who} in ${where} is missing permissions a deployment needs. Open ${link} to see which.`
    default:
      return `The ${cloudName} connection "${c.name}"${who} in ${where} is healthy.`
  }
}
