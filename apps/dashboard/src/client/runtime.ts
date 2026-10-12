/**
 * Points @forge-go/client-core at Loomplane, with the authsome session on
 * every request.
 *
 * Hand-written, and outside forge/ on purpose: everything under forge/ is
 * generated, and `forge client check` reports any file it didn't write there
 * as drift. The shape follows tap's (github.com/xraph/tap-frontend,
 * packages/client/forge-runtime.ts).
 */
import type {
  AuthProvider,
  OperationMeta,
  RestClientLike,
  RestRequestConfig,
} from "@forge-go/client-core"
import { configureClient, RestTransport } from "@forge-go/client-core"

import { entities as authsomeEntities } from "./forge/authsome/entities"
import {
  HTTPClient as AuthsomeHTTPClient,
  type RequestConfig,
} from "./forge/authsome/fetch"
import { entities as loomplaneEntities } from "./forge/loomplane/entities"
import { HTTPClient as LoomplaneHTTPClient } from "./forge/loomplane/fetch"
import { sessionToken } from "./session-token"

const REQUEST_TIMEOUT_MS = 30_000

/** The header a tab sends with the organization it believes it's in. */
export const ORG_HEADER = "X-Loomplane-Org"

/**
 * One generated HTTP client per service, chosen by path prefix. Each carries
 * its own codec table, so a response decoded by the wrong one would pass
 * through un-renamed: typed camelCase, populated snake_case, no error.
 */
const SERVICES = [
  {
    prefix: "/api/",
    client: new LoomplaneHTTPClient("", REQUEST_TIMEOUT_MS),
    orgScoped: true,
  },
  {
    prefix: "/authsome/",
    client: new AuthsomeHTTPClient("", REQUEST_TIMEOUT_MS),
    // authsome scopes by the org in the path and checks membership itself.
    orgScoped: false,
  },
] as const

export function serviceFor(path: string) {
  const service = SERVICES.find((s) => path.startsWith(s.prefix))
  if (!service) {
    throw new Error(
      `client runtime: no generated client owns "${path}"; paths start with ${SERVICES.map((s) => s.prefix).join(" or ")}`
    )
  }
  return service
}

const restClient: RestClientLike = {
  request<T>(config: RestRequestConfig): Promise<T> {
    const { client } = serviceFor(config.url)
    // Every codec on the config was put there by a generated op, so it is
    // already the narrow form the generated fetch expects.
    return client.request<T>(config as RequestConfig)
  },
}

let tabOrganization = ""
let publishableKey = ""
let authsomeAppID = ""
let refresher: (() => Promise<void>) | null = null

/** setTabOrganization records the organization this tab acts in. */
export function setTabOrganization(orgId: string): void {
  tabOrganization = orgId
}

/**
 * setAuthsomeApp names the authsome app: its publishable key goes on every
 * request to authsome, and a few calls take its ID as a parameter.
 */
export function setAuthsomeApp(app: {
  publishableKey: string
  appId: string
}): void {
  publishableKey = app.publishableKey
  authsomeAppID = app.appId
}

/** authsomeApp is the authsome app's ID, for calls that take it. */
export function authsomeApp(): string {
  return authsomeAppID
}

/**
 * setSessionRefresher hands the runtime the authsome SDK's renewal, so a 401
 * renews through the SDK, which owns the refresh token and holds a cross-tab
 * lock while it rotates it. Registered by <SessionRefreshBridge>.
 */
export function setSessionRefresher(next: (() => Promise<void>) | null): void {
  refresher = next
}

/**
 * What a 401 does. Resolving retries the request; rejecting lets the 401
 * stand. The transport calls this once for a whole burst of failures.
 */
async function renewSession(): Promise<void> {
  const before = sessionToken()
  if (!refresher) {
    throw new Error("no session refresher is registered")
  }
  await refresher()
  const after = sessionToken()
  if (!after || after === before) {
    throw new Error("the session could not be renewed")
  }
}

const auth: AuthProvider = {
  credentials(meta: OperationMeta) {
    const headers: Record<string, string> = {}
    // Read per request: the SDK rotates the token under a long-lived tab.
    const token = sessionToken()
    if (token) headers.Authorization = `Bearer ${token}`
    const service = serviceFor(meta.path)
    if (service.orgScoped && tabOrganization) {
      headers[ORG_HEADER] = tabOrganization
    }
    if (!service.orgScoped && publishableKey) {
      headers["X-Publishable-Key"] = publishableKey
    }
    return headers
  },
  refresh: renewSession,
}

/** The dashboard's one normalized cache over both services. */
export const forgeClient = configureClient({
  transport: new RestTransport({ client: restClient, auth }),
  entities: { ...authsomeEntities, ...loomplaneEntities },
})
