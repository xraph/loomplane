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
