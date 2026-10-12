const dateFormat = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
})

/** formatDate shows an API timestamp in the reader's locale and zone. */
export function formatDate(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : dateFormat.format(d)
}

/** repoLabel shortens a repository URL to host/org/repo. */
export function repoLabel(url: string): string {
  return url
    .replace(/^https:\/\//, "")
    .replace(/^git@([^:]+):/, "$1/")
    .replace(/\.git$/, "")
}
