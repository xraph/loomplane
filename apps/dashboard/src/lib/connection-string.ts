// Reading a connection string in the browser, to show what it points at
// before anyone checks it. Nothing here leaves the page: the parts are for
// display, the password is never returned, and the string itself goes to
// the server only when the person checks or registers it.

import { parseDotenv } from "@/lib/dotenv"

/** The engines a workspace service can be registered as by its URL. */
export type ConnectionEngine = "postgres" | "mysql" | "redis" | "mongodb"

export const connectionEngines: ConnectionEngine[] = [
  "postgres",
  "mysql",
  "redis",
  "mongodb",
]

export const engineTitles: Record<ConnectionEngine, string> = {
  postgres: "PostgreSQL",
  mysql: "MySQL",
  redis: "Redis",
  mongodb: "MongoDB",
}

const defaultPorts: Record<ConnectionEngine, number> = {
  postgres: 5432,
  mysql: 3306,
  redis: 6379,
  mongodb: 27017,
}

/** The URL schemes each engine takes, the first the one to write. */
const schemes: Record<ConnectionEngine, string[]> = {
  postgres: ["postgres", "postgresql"],
  mysql: ["mysql"],
  redis: ["redis", "rediss"],
  mongodb: ["mongodb", "mongodb+srv"],
}

/** TLS as the server words it. */
export type ConnectionTLS = "disable" | "require" | "verify-full"

/** What a connection string points at, without its password. */
export interface ConnectionParts {
  engine: ConnectionEngine
  scheme: string
  host: string
  port: number
  /** Every host, for a MongoDB replica set. */
  hosts: string[]
  user: string
  hasPassword: boolean
  database: string
  /** Undefined when the string doesn't say, which some engines need. */
  tls?: ConnectionTLS
  params: Record<string, string>
}

/** engineOf is the engine a connection string's scheme names, if any. */
export function engineOf(dsn: string): ConnectionEngine | undefined {
  const scheme = dsn.trim().split("://")[0]?.toLowerCase()
  return connectionEngines.find((e) => schemes[e].includes(scheme))
}

function decode(s: string): string {
  try {
    return decodeURIComponent(s)
  } catch {
    return s
  }
}

/**
 * parseConnection reads a connection string's parts for display. It
 * returns undefined for anything that isn't a URL of a known engine. It
 * never returns the password, only whether there is one.
 */
export function parseConnection(dsn: string): ConnectionParts | undefined {
  const text = dsn.trim()
  const engine = engineOf(text)
  if (!engine) return undefined
  const [scheme, rest = ""] = text.split(/:\/\/(.*)/s)
  // The user info ends at the last @, as the server reads it; the host
  // ends at the first / or ? after that.
  const at = rest.lastIndexOf("@")
  const tail = rest.slice(at + 1)
  const slash = tail.search(/[/?]/)
  const end = slash < 0 ? rest.length : at + 1 + slash
  const info = at >= 0 ? rest.slice(0, at) : ""
  const hostPart = rest.slice(at + 1, end)
  const after = rest.slice(end)
  const [pathPart, query = ""] = after.split(/\?(.*)/s)
  const [rawUser, ...password] = info.split(":")
  const hosts = hostPart.split(",").filter(Boolean)
  if (hosts.length === 0) return undefined
  const first = hosts[0]
  const bracket = first.match(/^\[([^\]]+)\](?::(\d+))?$/)
  const plain = first.match(/^([^:]+)(?::(\d+))?$/)
  const host = bracket?.[1] ?? plain?.[1] ?? first
  const port = Number(bracket?.[2] ?? plain?.[2] ?? defaultPorts[engine])
  const params: Record<string, string> = {}
  for (const pair of query.split("&").filter(Boolean)) {
    const [k, v = ""] = pair.split("=")
    params[decode(k)] = decode(v)
  }
  return {
    engine,
    scheme: scheme.toLowerCase(),
    host: decode(host),
    port,
    hosts: hosts.map(decode),
    user: decode(rawUser ?? ""),
    hasPassword: password.length > 0 && password.join(":") !== "",
    database: decode(pathPart.replace(/^\//, "")),
    tls: tlsOf(engine, scheme.toLowerCase(), params),
    params,
  }
}

function tlsOf(
  engine: ConnectionEngine,
  scheme: string,
  params: Record<string, string>
): ConnectionTLS | undefined {
  switch (engine) {
    case "postgres":
    case "mysql": {
      const mode = params.sslmode
      return mode === "disable" || mode === "require" || mode === "verify-full"
        ? mode
        : undefined
    }
    case "redis":
      return scheme === "rediss" ? "verify-full" : "disable"
    case "mongodb": {
      const on =
        params.tls === "true" ||
        params.ssl === "true" ||
        (scheme === "mongodb+srv" && params.tls !== "false")
      if (!on) return "disable"
      return params.tlsInsecure === "true" ||
        params.tlsAllowInvalidCertificates === "true"
        ? "require"
        : "verify-full"
    }
  }
}

/**
 * maskConnection is the string with its password replaced, for showing
 * back what was pasted.
 */
export function maskConnection(dsn: string): string {
  return dsn
    .trim()
    .replace(/^([a-z+]+:\/\/[^:/?#@]*:)([^@]*)(@)/i, "$1••••••$3")
}

/** The parts someone types instead of pasting a string. */
export interface ConnectionFields {
  engine: ConnectionEngine
  host: string
  port: string
  user: string
  password: string
  database: string
  tls: ConnectionTLS
}

/**
 * buildConnection writes fields as the connection string the server
 * takes, escaping the user and password.
 */
export function buildConnection(f: ConnectionFields): string {
  const user = encodeURIComponent(f.user)
  const auth = f.user
    ? `${user}${f.password ? `:${encodeURIComponent(f.password)}` : ""}@`
    : f.password
      ? `:${encodeURIComponent(f.password)}@`
      : ""
  const host = f.host.includes(":") ? `[${f.host}]` : f.host
  const port = f.port.trim() ? `:${f.port.trim()}` : ""
  const db = f.database.trim()
  switch (f.engine) {
    case "postgres":
    case "mysql":
      return `${f.engine}://${auth}${host}${port}/${encodeURIComponent(db)}?sslmode=${f.tls}`
    case "redis":
      return `${f.tls === "disable" ? "redis" : "rediss"}://${auth}${host}${port}${db ? `/${db}` : ""}`
    case "mongodb": {
      const params = new URLSearchParams()
      if (f.tls !== "disable") params.set("tls", "true")
      if (f.tls === "require") params.set("tlsAllowInvalidCertificates", "true")
      if (f.user) params.set("authSource", db || "admin")
      const q = params.toString()
      return `mongodb://${auth}${host}${port}/${encodeURIComponent(db)}${q ? `?${q}` : ""}`
    }
  }
}

/** A mistake worth fixing before checking, and the fix when there's one. */
export interface ConnectionHint {
  message: string
  /** The string with the fix applied, offered as a one-click change. */
  fix?: { label: string; dsn: string }
}

const local = /^(localhost|127\.\d+\.\d+\.\d+|::1|0\.0\.0\.0)$/i

/**
 * connectionHints are what the server would refuse, or what won't work
 * from a target, said before the check runs.
 */
export function connectionHints(dsn: string): ConnectionHint[] {
  const p = parseConnection(dsn)
  if (!p) return []
  const out: ConnectionHint[] = []
  if (local.test(p.host))
    out.push({
      message: `${p.host} is the machine Loomplane checks from, not yours. Use an address your servers can reach.`,
    })
  if (p.engine !== "redis" && !p.user)
    out.push({
      message: `A ${engineTitles[p.engine]} connection string names its user.`,
    })
  if ((p.engine === "postgres" || p.engine === "mysql") && !p.tls) {
    const join = dsn.includes("?") ? "&" : "?"
    out.push({
      message:
        p.engine === "postgres"
          ? "Say how it's reached: sslmode=require, verify-full or disable."
          : "MySQL is reached over TLS: add sslmode=require or verify-full.",
      fix: {
        label: "Add sslmode=require",
        dsn: `${dsn.trim()}${join}sslmode=require`,
      },
    })
  }
  if (p.engine === "mysql" && p.tls === "disable")
    out.push({
      message: "MySQL is reached over TLS: use sslmode=require or verify-full.",
    })
  if (
    p.engine === "mongodb" &&
    p.hosts.length > 1 &&
    p.scheme === "mongodb+srv"
  )
    out.push({ message: "A mongodb+srv string names one host." })
  // Two @ before the host: the password holds an unescaped one.
  const info = dsn.trim().split("://")[1]?.split(/[/?]/)[0] ?? ""
  if ((info.match(/@/g) ?? []).length > 1)
    out.push({
      message: "Escape any @ in the password as %40, so the host reads right.",
    })
  return out
}

/**
 * suggestName is a name for a service from where it points: its
 * database, else the first part of its host.
 */
export function suggestName(p: ConnectionParts): string {
  const raw = p.database || p.host.split(".")[0] || p.engine
  return (
    raw
      .toLowerCase()
      .replace(/[^a-z0-9-]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 60) || p.engine
  )
}

/** A connection string found in pasted .env text, by its key. */
export interface FoundConnection {
  key: string
  value: string
  engine: ConnectionEngine
}

/**
 * connectionsIn finds the values that are connection strings in pasted
 * text: KEY=value lines, or one bare string.
 */
export function connectionsIn(text: string): FoundConnection[] {
  const trimmed = text.trim()
  const bare = engineOf(trimmed)
  if (bare && !/\s/.test(trimmed))
    return [{ key: "", value: trimmed, engine: bare }]
  return parseDotenv(text).entries.flatMap((e) => {
    const engine = engineOf(e.value)
    return engine ? [{ key: e.key, value: e.value.trim(), engine }] : []
  })
}

/** defaultPort is the port an engine listens on unless told otherwise. */
export function defaultPort(engine: ConnectionEngine): number {
  return defaultPorts[engine]
}
