/**
 * Points @forge-go/client-core at Loomplane. The authsome session rides on
 * every request as its httpOnly cookie, which the browser sends to the same
 * origin by itself, so no token passes through here.
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
import { op_retentionPolicies_set } from "./forge/loomplane/ops/retentionPolicies.set"
import { HTTPClient as LoomplaneHTTPClient } from "./forge/loomplane/fetch"

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
let refresher: (() => Promise<boolean>) | null = null

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
 * setSessionRefresher hands the runtime the authsome SDK's check of the
 * session, which asks the backend whether it still knows the cookie and
 * resolves to whether it does. Registered by <SessionRefreshBridge>.
 */
export function setSessionRefresher(
  next: (() => Promise<boolean>) | null
): void {
  refresher = next
}

/**
 * What a 401 does. Resolving retries the request; rejecting lets the 401
 * stand. The backend renews the session cookie as requests use it, so a 401
 * means the session may be over: the SDK asks the backend, and only a
 * session it still knows is worth a retry. When it doesn't, the SDK is
 * signed out too, and the dashboard shows sign-in. The transport calls this
 * once for a whole burst of failures.
 */
export async function renewSession(): Promise<void> {
  if (!refresher) {
    throw new Error("no session refresher is registered")
  }
  if (!(await refresher())) {
    throw new Error("the session has ended")
  }
}

/**
 * headersFor is what a request to path carries besides its cookie: the
 * tab's organization for Loomplane, the publishable key for authsome.
 */
function headersFor(path: string): Record<string, string> {
  const headers: Record<string, string> = {}
  const service = serviceFor(path)
  if (service.orgScoped && tabOrganization) {
    headers[ORG_HEADER] = tabOrganization
  }
  if (!service.orgScoped && publishableKey) {
    headers["X-Publishable-Key"] = publishableKey
  }
  return headers
}

const auth: AuthProvider = {
  credentials(meta: OperationMeta) {
    return headersFor(meta.path)
  },
  refresh: renewSession,
}

const transport = new RestTransport({ client: restClient, auth })
// An uncertain revision-guarded save needs inspection before another send.
const policyTransport = new RestTransport({
  client: restClient,
  auth,
  retry: { attempts: 1 },
})

/** The dashboard's one normalized cache over both services. */
export const forgeClient = configureClient({
  transport: {
    execute(request) {
      const policySave =
        request.meta.method === op_retentionPolicies_set.method &&
        request.meta.path === op_retentionPolicies_set.path
      return (policySave ? policyTransport : transport).execute(request)
    },
  },
  entities: {
    ...authsomeEntities,
    ...loomplaneEntities,
    // A review and its sync retain immutable before/after snapshots. These
    // share the live target ID but must never upsert or reference its cache row.
    Preview: { idField: "id" },
    Sync: { idField: "id" },
  },
})
