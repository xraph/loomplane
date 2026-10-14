// Plans and deployments come a page at a time, newest first, each page
// ending in a cursor that reads the next older one. These join a project's
// pages into one list and work out what still has to be read so that what's
// shown has no holes: across pages, across projects, and between a plan and
// its deployment.

export interface Page<T> {
  items: T[]
  next?: string
}

export interface Joined<T> {
  items: T[]
  /** next reads older rows; undefined when there are none or a page is loading. */
  next?: string
  loading: boolean
}

/**
 * joinPages joins one list's pages, newest first. A row two pages share is
 * kept once, as the earlier page has it: the first page is the one kept
 * live, so its copy is the current one.
 */
export function joinPages<T extends { id: string }>(
  pages: (Page<T> | undefined)[]
): Joined<T> {
  const seen = new Set<string>()
  const items: T[] = []
  for (const p of pages) {
    for (const it of p?.items ?? []) {
      if (seen.has(it.id)) continue
      seen.add(it.id)
      items.push(it)
    }
  }
  const loading = pages.some((p) => !p)
  return { items, next: loading ? undefined : pages.at(-1)?.next, loading }
}

/**
 * bridgeFrom is where to read the rows a refreshed first page no longer
 * holds. Older pages were read from the cursor the first page ended at then;
 * once a new row pushes that page's end newer, the rows in between are in
 * neither, and a page read from the first page's current end fills them.
 */
export function bridgeFrom(
  firstNext: string | undefined,
  older: string[]
): string | undefined {
  if (!firstNext || older.length === 0 || firstNext === older[0])
    return undefined
  return firstNext
}

/**
 * horizon is the newest time any list still has older rows past: the oldest
 * row loaded from each list with more. Rows older than it may have unread
 * neighbours in another project, so a merged list is only whole down to
 * here. undefined means everything is loaded.
 */
export function horizon(oldest: (string | undefined)[]): string | undefined {
  let best: string | undefined
  for (const t of oldest) {
    if (
      t !== undefined &&
      (best === undefined || Date.parse(t) > Date.parse(best))
    )
      best = t
  }
  return best
}

/**
 * needsOlder reports whether a project's deployments must be read further
 * back so every loaded plan that was approved shows its deployment. Only
 * an approved plan has one, and it starts after the plan is made, so the
 * oldest approved plan still without one is the point to read back past.
 */
export function needsOlder(
  deployments: Joined<{ planId: string; startedAt: string }>,
  plans: { id: string; status: string; createdAt: string }[]
): boolean {
  if (deployments.loading || !deployments.next) return false
  const have = new Set(deployments.items.map((d) => d.planId))
  const waiting = plans.findLast(
    (p) => p.status === "approved" && !have.has(p.id)
  )
  const last = deployments.items.at(-1)
  return (
    !!waiting &&
    !!last &&
    Date.parse(last.startedAt) >= Date.parse(waiting.createdAt)
  )
}
