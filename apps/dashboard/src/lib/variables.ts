// Environments and the layered variables set on them, made readable.
// Mirrors internal/environments: the API decides, this only explains.

import type { Variable } from "@/client/forge/loomplane/types"

export const environmentKinds = [
  {
    value: "production",
    label: "Production",
    description: "Serves your users.",
  },
  {
    value: "staging",
    label: "Staging",
    description: "A copy of production to try changes on first.",
  },
  {
    value: "preview",
    label: "Preview",
    description: "Short-lived deployments of branches and pull requests.",
  },
  {
    value: "development",
    label: "Development",
    description: "For building and testing as you go.",
  },
] as const

/** kindFor guesses an environment's kind from its name, as the API does. */
export function kindFor(name: string): string {
  return environmentKinds.some((k) => k.value === name) ? name : "development"
}

/** Where a value is set: the whole project, or one service. */
export interface Target {
  /** A service ID from the latest analysis, or "" for the whole project. */
  service: string
  serviceName?: string
}

const scopeLabels: Record<string, string> = {
  organization: "Workspace",
  project: "Project",
  service: "Service",
}

/** layerLabel names the layer a variable is set on. */
export function layerLabel(v: Variable): string {
  return scopeLabels[v.scope] ?? v.scope
}

/** reachLabel says which environments a variable applies in. */
export function reachLabel(v: Variable): string {
  return v.environment ? `${v.environment} only` : "every environment"
}

/**
 * ownLayer reports whether v is set on the layer being looked at, so it's
 * edited here, rather than inherited and only overridden here.
 */
export function ownLayer(v: Variable, target: Target): boolean {
  if (target.service) {
    return v.scope === "service" && v.service === target.service
  }
  return v.scope === "project"
}

/** keyValid is the API's rule for a key: letters, digits, underscores. */
export function keyValid(key: string): boolean {
  return /^[A-Za-z_][A-Za-z0-9_]*$/.test(key) && key.length <= 255
}

/**
 * looksSecret guesses whether a variable should be set as a secret, from
 * its key and, when there is one, its value. It's only the starting point
 * of the Secret switch an import shows: people correct it before setting.
 *
 * Getting it wrong costs differently each way. A secret marked plain is
 * shown to everyone who can see the project's variables; a plain value
 * marked secret can't be read back, which is only an annoyance.
 */
export function looksSecret(key: string, value = ""): boolean {
  const k = key.toUpperCase()
  // A bundler ships these to every browser, so they can't be kept secret.
  if (browserPrefixes.some((p) => k.startsWith(p))) return false
  if (secretValues.some((re) => re.test(value))) return true
  if (addressKeys.test(k)) return true
  if (notSecretKeys.test(k)) return false
  return secretKeys.test(k)
}

/** Prefixes bundlers inline into client code. */
const browserPrefixes = [
  "NEXT_PUBLIC_",
  "VITE_",
  "REACT_APP_",
  "EXPO_PUBLIC_",
  "NUXT_PUBLIC_",
  "GATSBY_",
  "PUBLIC_",
]

/** Values that are credentials whatever their key: tokens, keys, passwords in URLs. */
const secretValues = [
  /^(sk|rk)_(live|test)_/, // Stripe secret and restricted keys
  /^(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}/, // GitHub tokens
  /^github_pat_/,
  /^xox[abpors]-/, // Slack tokens
  /^sk-[A-Za-z0-9_-]{20,}/, // OpenAI-style API keys
  /-----BEGIN [A-Z ]*PRIVATE KEY-----/,
  /^[a-z][a-z0-9+.-]*:\/\/[^/\s:@]*:[^/\s@]+@/i, // a URL with a password
]

/**
 * Connection strings, which carry their credentials. Loomplane sets the
 * ones it provisions as secrets too.
 */
const addressKeys =
  /(DATABASE_URL$|^(MONGODB_URI|MONGO_URL|REDIS_URL|AMQP_URL)$|_DSN$|^DSN$|CONNECTION_STRING)/

/**
 * Keys that name a secret thing without being one: its ID, its public
 * half, where it's kept, or how long it lasts.
 */
const notSecretKeys =
  /(PUBLIC|PUBLISHABLE|_ID$|_PATH$|_FILE$|_DIR$|_TTL$|_EXPIR|_LENGTH$|_ALGORITHM$|_ISSUER$|_URL$)/

/** Words a secret's key holds. */
const secretKeys =
  /(SECRET|TOKEN|PASSWORD|PASSWD|PASSPHRASE|PRIVATE|CREDENTIAL|API_?KEY|ACCESS_KEY|_KEY$|^KEY$|SALT|SIGNING|ENCRYPTION)/
