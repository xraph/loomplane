// How a service reads a database or cache it needs: in one variable with
// the whole connection string, or in separate ones for its host, port,
// user, password and database. What it already reads in its code, as
// analysis found, decides where the form starts.

/** What a wired variable holds. */
export type WiringOutput =
  "url" | "host" | "port" | "user" | "password" | "database"

export const partOutputs: Exclude<WiringOutput, "url">[] = [
  "host",
  "port",
  "user",
  "password",
  "database",
]

export const outputLabels: Record<WiringOutput, string> = {
  url: "Connection string",
  host: "Host",
  port: "Port",
  user: "User",
  password: "Password",
  database: "Database",
}

/** Variables by name, each with what it holds, as loomplane.yaml's wiring.<need>.env. */
export type WiringEnv = Record<string, WiringOutput>

/** The kinds whose address has parts a variable can hold. */
const families: Record<string, "postgres" | "mysql" | "redis" | "mongodb"> = {
  postgres: "postgres",
  mysql: "mysql",
  mariadb: "mysql",
  redis: "redis",
  valkey: "redis",
  mongodb: "mongodb",
}

/** wirable reports whether a kind's variables can be chosen. */
export function wirable(kind?: string): boolean {
  return !!kind && kind in families
}

/** A named set of variables, one per part. */
export type PartTemplate = {
  id: string
  label: string
  vars: Partial<Record<Exclude<WiringOutput, "url">, string>>
}

const familyTemplates: Record<string, PartTemplate> = {
  postgres: {
    id: "libpq",
    label: "PGHOST, PGPORT…",
    vars: {
      host: "PGHOST",
      port: "PGPORT",
      user: "PGUSER",
      password: "PGPASSWORD",
      database: "PGDATABASE",
    },
  },
  mysql: {
    id: "mysql",
    label: "MYSQL_HOST, MYSQL_PORT…",
    vars: {
      host: "MYSQL_HOST",
      port: "MYSQL_PORT",
      user: "MYSQL_USER",
      password: "MYSQL_PASSWORD",
      database: "MYSQL_DATABASE",
    },
  },
  redis: {
    id: "redis",
    label: "REDIS_HOST, REDIS_PORT…",
    vars: {
      host: "REDIS_HOST",
      port: "REDIS_PORT",
      password: "REDIS_PASSWORD",
      database: "REDIS_DB",
    },
  },
  mongodb: {
    id: "mongodb",
    label: "MONGODB_HOST, MONGODB_PORT…",
    vars: {
      host: "MONGODB_HOST",
      port: "MONGODB_PORT",
      user: "MONGODB_USER",
      password: "MONGODB_PASSWORD",
      database: "MONGODB_DATABASE",
    },
  },
}

/** prefixTemplate is DB_HOST, DB_PORT and so on, under any prefix. */
export function prefixTemplate(prefix: string): PartTemplate {
  const p = prefix.toUpperCase().replace(/[^A-Z0-9_]/g, "")
  return {
    id: "prefix",
    label: `${p}HOST, ${p}PORT…`,
    vars: {
      host: `${p}HOST`,
      port: `${p}PORT`,
      user: `${p}USER`,
      password: `${p}PASSWORD`,
      database: `${p}NAME`,
    },
  }
}

/** templatesFor are the ready-made sets for a kind: its engine's own, then a prefix. */
export function templatesFor(kind: string, prefix = "DB_"): PartTemplate[] {
  const family = families[kind]
  return [...(family ? [familyTemplates[family]] : []), prefixTemplate(prefix)]
}

/** The names code commonly reads a whole connection string from, by engine. */
const urlNames: Record<string, string[]> = {
  postgres: [
    "DATABASE_URL",
    "POSTGRES_URL",
    "POSTGRESQL_URL",
    "PG_URL",
    "PG_DSN",
    "DATABASE_DSN",
    "DB_URL",
    "DB_DSN",
    "DATABASE_URI",
  ],
  mysql: [
    "DATABASE_URL",
    "MYSQL_URL",
    "MYSQL_DSN",
    "DATABASE_DSN",
    "DB_URL",
    "DB_DSN",
    "DATABASE_URI",
  ],
  redis: ["REDIS_URL", "REDIS_URI", "REDIS_DSN", "CACHE_URL", "VALKEY_URL"],
  mongodb: [
    "MONGODB_URI",
    "MONGODB_URL",
    "MONGO_URI",
    "MONGO_URL",
    "DATABASE_URL",
  ],
}

/** urlNamesFor are the names a whole connection string is usually read from. */
export function urlNamesFor(kind: string): string[] {
  return urlNames[families[kind] ?? ""] ?? []
}

/** How the form starts for one service. */
export type WiringChoice =
  | { mode: "one"; key: string }
  | {
      mode: "parts"
      template: string
      prefix: string
      vars: Partial<Record<Exclude<WiringOutput, "url">, string>>
    }

/** A starting choice and why. */
export type Suggestion = {
  choice: WiringChoice
  /** The variables the code reads that it was chosen for. */
  detected: string[]
}

/**
 * suggestWiring is where the form starts for a service that reads the
 * names in reads: the engine's separate variables when the code reads two
 * or more of them, else a connection string it reads by one of the usual
 * names, else the kind's own variable.
 */
export function suggestWiring(
  kind: string,
  fallback: string,
  reads: string[]
): Suggestion {
  const read = new Set(reads.map((r) => r.toUpperCase()))
  for (const t of templatesFor(kind)) {
    const found = Object.entries(t.vars).filter(
      ([, name]) => name && read.has(name)
    )
    if (found.length >= 2)
      return {
        choice: {
          mode: "parts",
          template: t.id,
          prefix: "DB_",
          vars: Object.fromEntries(found),
        },
        detected: found.map(([, name]) => name as string),
      }
  }
  const url = urlNamesFor(kind).find((n) => read.has(n))
  if (url) return { choice: { mode: "one", key: url }, detected: [url] }
  return { choice: { mode: "one", key: fallback }, detected: [] }
}

/**
 * envOf is the wiring a choice writes, or undefined when it's the kind's
 * own variable, which needs none.
 */
export function envOf(
  choice: WiringChoice,
  fallback: string
): WiringEnv | undefined {
  if (choice.mode === "one") {
    const key = choice.key.trim()
    return key && key !== fallback ? { [key]: "url" } : undefined
  }
  const env: WiringEnv = {}
  for (const output of partOutputs) {
    const name = choice.vars[output]?.trim()
    if (name) env[name] = output
  }
  return Object.keys(env).length ? env : undefined
}

const variableName = /^[A-Za-z_][A-Za-z0-9_]{0,254}$/

/** wiringProblem is what's wrong with a choice, or "". */
export function wiringProblem(choice: WiringChoice): string {
  const names =
    choice.mode === "one"
      ? [choice.key.trim()]
      : Object.values(choice.vars)
          .map((v) => (v ?? "").trim())
          .filter(Boolean)
  if (names.length === 0) return "Name at least one variable."
  const bad = names.find((n) => !variableName.test(n))
  if (bad !== undefined)
    return `${bad || "An empty name"} isn't a variable name: use letters, digits and underscores, not starting with a digit.`
  const twice = names.find((n, i) => names.indexOf(n) !== i)
  if (twice) return `${twice} is used twice.`
  return ""
}

/** How a service gets into a workspace service, as loomplane.yaml's wiring.<need>.access. */
export type Access = "shared" | "own_database" | "own_user"

/** An access mode and the service's own database under it. */
export type AccessChoice = { access: Access; database: string }

/** What a workspace service's credentials may do, as its last check found. */
export type AccessRights = {
  engine: string
  /** Whether it's a service registered by its connection string. */
  registered: boolean
  createDatabase: boolean
  adminSet: boolean
  adminCreateDatabase: boolean
  adminCreateUser: boolean
}

/** A registered workspace service's rights, from what the API says of it. */
export function accessRights(e: {
  engine: string
  origin?: string
  adminSet?: boolean
  lastCheck?: {
    capabilities?: { createDatabase: boolean; createUser: boolean }
    admin?: { createDatabase: boolean; createUser: boolean }
  }
}): AccessRights {
  return {
    engine: e.engine,
    registered: !e.origin || e.origin === "registered",
    createDatabase: !!e.lastCheck?.capabilities?.createDatabase,
    adminSet: !!e.adminSet,
    adminCreateDatabase: !!e.lastCheck?.admin?.createDatabase,
    adminCreateUser: !!e.lastCheck?.admin?.createUser,
  }
}

/** One access mode as the form offers it. */
export type AccessOption = {
  access: Access
  label: string
  /** Why it can't be chosen, or "". */
  unavailable: string
  /** What choosing it means that someone should know first, or "". */
  note: string
}

const ownDatabaseEngines = ["postgres", "mysql", "redis", "mongodb"]

/** accessOptions are the access modes a workspace service can give. */
export function accessOptions(
  rights: AccessRights,
  database: string
): AccessOption[] {
  const sql = rights.engine === "postgres" || rights.engine === "mysql"
  return [
    { access: "shared", label: "Same credentials", unavailable: "", note: "" },
    {
      access: "own_database",
      label: "Its own database",
      unavailable: !ownDatabaseEngines.includes(rights.engine)
        ? `A ${rights.engine} service has no databases to give.`
        : "",
      note:
        sql && !rights.createDatabase
          ? `These credentials can't create databases, so ${database || "the database"} must already exist.`
          : "",
    },
    {
      access: "own_user",
      label: "Its own user",
      unavailable: !sql
        ? "Loomplane makes users on Postgres and MySQL only."
        : !rights.adminSet
          ? "Add an admin credential on the workspace service's page first."
          : !rights.adminCreateUser
            ? "Its admin credential can't create users."
            : "",
      note:
        sql && rights.adminSet && !rights.adminCreateDatabase
          ? `The admin credential can't create databases, so ${database || "the database"} must already exist.`
          : "",
    },
  ]
}

/** ownDatabaseName is the database a service gets when it names none. */
export function ownDatabaseName(service: string): string {
  return service
    .toLowerCase()
    .replace(/[^a-z0-9_]/g, "_")
    .slice(0, 63)
}

/**
 * ownUserName is the user Loomplane makes for a project's service:
 * lp_<project>_<service>. A longer one than 32 characters is shortened
 * with a hash the server adds, so it's undefined here.
 */
export function ownUserName(
  projectSlug: string,
  service: string
): string | undefined {
  const full = `lp_${projectSlug}_${service}`
    .toLowerCase()
    .replace(/[^a-z0-9_]/g, "_")
  return full.length <= 32 ? full : undefined
}

/** accessProblem is what's wrong with an access choice, or "". */
export function accessProblem(choice: AccessChoice, engine: string): string {
  if (choice.access === "shared") return ""
  const db = choice.database.trim()
  if (engine === "redis") {
    if (!/^\d{1,4}$/.test(db))
      return "Redis databases are numbered: choose one, such as 1."
    return ""
  }
  if (db && !/^[A-Za-z0-9_]{1,63}$/.test(db))
    return "A database name is up to 63 letters, digits and underscores."
  return ""
}

/** The body of one service's wiring, as the manifest edits take it. */
export type WiringBody = {
  access?: Access
  database?: string
  env?: Record<string, WiringOutput>
}

/**
 * wiringBody is what a service's choices write, or undefined when it reads
 * the shared credentials in the kind's own variable, which needs none.
 */
export function wiringBody(
  choice: WiringChoice,
  fallback: string,
  access?: AccessChoice
): WiringBody | undefined {
  const env = envOf(choice, fallback)
  const own = access && access.access !== "shared"
  if (!env && !own) return undefined
  return {
    ...(own ? { access: access.access } : {}),
    ...(own && access.database.trim()
      ? { database: access.database.trim() }
      : {}),
    ...(env ? { env } : {}),
  }
}

/**
 * choiceOf is the form's starting point for a wiring loomplane.yaml has:
 * one variable when it names one url, else the separate variables, under
 * the engine's names when they match.
 */
export function choiceOf(
  kind: string,
  fallback: string,
  env?: Record<string, string>
): WiringChoice {
  const entries = Object.entries(env ?? {})
  if (entries.length === 0) return { mode: "one", key: fallback }
  if (entries.length === 1 && entries[0][1] === "url")
    return { mode: "one", key: entries[0][0] }
  const vars: Partial<Record<Exclude<WiringOutput, "url">, string>> = {}
  for (const [name, output] of entries)
    if (output !== "url") vars[output as Exclude<WiringOutput, "url">] = name
  const same = (t: PartTemplate) =>
    partOutputs.every((o) => (t.vars[o] ?? "") === (vars[o] ?? ""))
  const known = templatesFor(kind).find((t) => t.id !== "prefix" && same(t))
  return {
    mode: "parts",
    template: known?.id ?? "custom",
    prefix: "DB_",
    vars,
  }
}

/** A variable someone set themselves that a wired one would lose to. */
export type HandSet = { key: string; environment: string }

/**
 * handSetFor are the variables set by hand for service, or for every
 * service in the project, that share a name with one it's wired to read.
 * A value set by hand wins over a linked one when it deploys.
 */
export function handSetFor(
  service: string,
  keys: string[],
  variables: {
    key: string
    service?: string
    environment: string
    resourceOutput?: string
  }[]
): HandSet[] {
  const wanted = new Set(keys)
  return variables
    .filter(
      (v) =>
        wanted.has(v.key) &&
        !v.resourceOutput &&
        (!v.service || v.service === service)
    )
    .map((v) => ({ key: v.key, environment: v.environment }))
}

/** keysOf are the variables a choice writes. */
export function keysOf(choice: WiringChoice): string[] {
  return choice.mode === "one"
    ? [choice.key.trim()].filter(Boolean)
    : Object.values(choice.vars)
        .map((v) => (v ?? "").trim())
        .filter(Boolean)
}
