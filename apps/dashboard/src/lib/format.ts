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

/**
 * repositoryURL turns what someone typed into the URL the API stores:
 * "owner/name" becomes a GitHub URL, and a full URL or git@ address is kept
 * as it is.
 */
export function repositoryURL(input: string): string {
  const v = input.trim()
  if (/^[\w.-]+\/[\w.-]+$/.test(v)) return `https://github.com/${v}`
  return v
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
