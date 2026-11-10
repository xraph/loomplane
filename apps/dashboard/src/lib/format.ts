const dateFormat = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
})

/** formatDate shows an API timestamp in the reader's locale and zone. */
export function formatDate(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : dateFormat.format(d)
}

/**
 * repositoriesLabel names a project's repositories in the room one takes:
 * the first, and how many more.
 */
export function repositoriesLabel(repos: string[]): string {
  if (repos.length === 0) return ""
  return repos.length === 1 ? repos[0] : `${repos[0]} +${repos.length - 1}`
}

/** repoLabel shortens a repository URL to host/org/repo. */
export function repoLabel(url: string): string {
  return url
    .replace(/^https:\/\//, "")
    .replace(/^git@([^:]+):/, "$1/")
    .replace(/\.git$/, "")
}

/**
 * githubRepo is "owner/name" for a GitHub repository URL or git@ address,
 * and empty for anything else. Analyses read GitHub, so the dashboard links
 * and labels repositories by this.
 */
export function githubRepo(url: string): string {
  const m = url
    .trim()
    .match(
      /^(?:https:\/\/github\.com\/|git@github\.com:)([\w.-]+\/[\w.-]+?)(?:\.git)?\/?$/
    )
  return m?.[1] ?? ""
}

const segmentPattern = /^[\w.-]+$/
// A host-qualified key: a host with a dot, or a name with a port such as
// localhost:8080, then at least a namespace and a name. GitHub's owner/name
// never has the host.
const qualifiedPattern =
  /^(?:[\w-]+(?:\.[\w-]+)+(?::\d+)?|[\w-]+:\d+)(?:\/[\w.-]+){2,}$/

/**
 * repositoryKey is the key a source stores for what someone pasted or typed,
 * and empty when it isn't a repository. A GitHub repository is "owner/name".
 * Any other host is "host/path", where the path keeps every group a GitLab
 * project sits under and is cut at its first "/-/" page.
 *
 * Page URLs give their repository, so a link to a file, branch or commit
 * works. The host is lowercased. The path keeps its case, except on
 * bitbucket.org, whose keys are stored lowercased.
 */
export function repositoryKey(input: string): string {
  const v = input.trim()
  if (!v) return ""
  if (/^[\w.-]+\/[\w.-]+$/.test(v)) return v.replace(/\.git$/, "")

  let host: string
  let path: string
  const url = v.match(
    /^([a-z][a-z0-9+.-]*):\/\/(?:[^/@\s]*@)?([^/\s]+)(\/\S*)?$/i
  )
  const scp = v.match(/^[\w.-]+@([^/:\s]+):(\S+)$/)
  if (url) {
    host = url[1].toLowerCase() === "ssh" ? url[2].replace(/:\d+$/, "") : url[2]
    path = url[3] ?? ""
  } else if (scp) {
    host = scp[1]
    path = scp[2]
  } else if (qualifiedPattern.test(v)) {
    const i = v.indexOf("/")
    host = v.slice(0, i)
    path = v.slice(i)
  } else {
    return ""
  }
  host = host.toLowerCase()

  const segments = path
    .replace(/[?#].*$/, "")
    .split("/")
    .filter(Boolean)
  const cut = segments.indexOf("-")
  const kept = cut < 0 ? segments : segments.slice(0, cut)

  const github = host === "github.com" || host === "www.github.com"
  let repo: string[]
  if (github) {
    repo = kept.slice(0, 2)
  } else if (host === "bitbucket.org" || host === "www.bitbucket.org") {
    host = "bitbucket.org"
    repo = kept.slice(0, 2).map((s) => s.toLowerCase())
  } else {
    repo = kept
  }
  if (repo.length < 2) return ""
  repo[repo.length - 1] = repo[repo.length - 1].replace(/\.git$/, "")
  if (!repo.every((s) => segmentPattern.test(s))) return ""
  const joined = repo.join("/")
  return github ? joined : `${host}/${joined}`
}

/**
 * startsWithHost is whether what's typed so far begins with a host, which
 * has a dot or a port that GitHub owners never do. A field adds github.com/
 * before everything else.
 */
export function startsWithHost(typed: string): boolean {
  const first = typed.trim().split("/")[0]
  return first.includes(".") || first.includes(":")
}

/**
 * repositoryHost is the host a stored key is on: "github.com" for a bare
 * owner/name, and the key's first segment otherwise.
 */
export function repositoryHost(key: string): string {
  const v = key.trim()
  return qualifiedPattern.test(v) ? v.slice(0, v.indexOf("/")) : "github.com"
}

/**
 * repositoryURL turns a stored key into its web URL: "owner/name" becomes a
 * GitHub URL and "host/path" an https one. A full URL or git@ address, and
 * anything that isn't a key yet, is kept as it is.
 */
export function repositoryURL(input: string): string {
  const v = input.trim()
  if (/^[\w.-]+\/[\w.-]+$/.test(v)) return `https://github.com/${v}`
  if (qualifiedPattern.test(v)) return `https://${v}`
  return v
}

/**
 * sourceURL links to a source on its host, at its directory when it has
 * one. Each host has its own page for a directory: GitHub's tree, GitLab's
 * -/tree (on any GitLab host) and Bitbucket's src. With no ref, HEAD.
 */
export function sourceURL(s: {
  repository: string
  root?: string
  ref?: string
}): string {
  const base = repositoryURL(s.repository)
  if (!s.root) return base
  const ref = s.ref || "HEAD"
  const host = repositoryHost(s.repository)
  if (host === "github.com") return `${base}/tree/${ref}/${s.root}`
  if (host === "bitbucket.org") return `${base}/src/${ref}/${s.root}`
  return `${base}/-/tree/${ref}/${s.root}`
}

/** hostLabel is what "Open on" names: GitHub, or the key's own host. */
export function hostLabel(repository: string): string {
  const host = repositoryHost(repository)
  return host === "github.com" ? "GitHub" : host
}

// Durations, relative times and amounts, shared with the mock (src/mock).

const second = 1000
const minute = 60 * second
const hour = 60 * minute
const day = 24 * hour

export function duration(ms: number): string {
  if (ms < second) return `${Math.max(0, Math.round(ms))}ms`
  const s = Math.floor(ms / second)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, "0")}s`
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, "0")}m`
}

export function relative(ts: number, now: number): string {
  const d = now - ts
  if (d < 45 * second) return "just now"
  if (d < hour) return `${Math.floor(d / minute)}m ago`
  if (d < day) return `${Math.floor(d / hour)}h ago`
  if (d < 2 * day) return "yesterday"
  if (d < 7 * day) return `${Math.floor(d / day)}d ago`
  return date(ts)
}

export function date(ts: number): string {
  return new Date(ts).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
  })
}

export function dateTime(ts: number): string {
  return new Date(ts).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  })
}

export function clock(ts: number): string {
  return new Date(ts).toLocaleTimeString(undefined, {
    hour12: false,
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  })
}

const usd = new Intl.NumberFormat(undefined, {
  style: "currency",
  currency: "USD",
  maximumFractionDigits: 0,
})
const usdCents = new Intl.NumberFormat(undefined, {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})

export function money(n: number, cents = false): string {
  return (cents ? usdCents : usd).format(n)
}

/** signedMoney shows a monthly cost change: "+$4.10" or "−$0.30". */
export function signedMoney(n: number): string {
  if (n === 0) return "$0.00"
  const s = usdCents.format(Math.abs(n))
  return n > 0 ? `+${s}` : `−${s}`
}

export function count(n: number): string {
  return n.toLocaleString()
}
