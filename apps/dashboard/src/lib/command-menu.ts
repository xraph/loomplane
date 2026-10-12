import type { Source } from "@/client/forge/loomplane/types"

/** projectIDFromPath reads the project a page belongs to, if it is one. */
export function projectIDFromPath(path: string): string | undefined {
  // The router has already decoded the pathname, so the id is used as it is.
  return /^\/projects\/([^/]+)/.exec(path)?.[1]
}

/** AnalyzeItem is one "Analyze …" entry in the command menu. */
export interface AnalyzeItem {
  key: string
  /** The source to analyze, or undefined for all of them. */
  alias?: string
  label: string
  /** What the entry will read, shown beside the label. */
  detail: string
  /** The words the menu's search matches on. */
  search: string
}

/**
 * analyzeItems lists what the command menu offers for analyzing a project's
 * repositories: one entry per live source, and one for all of them when there
 * are two or more. Someone who can't run analyses gets nothing.
 */
export function analyzeItems(
  sources: readonly Source[],
  mayRun: boolean
): AnalyzeItem[] {
  if (!mayRun) return []
  const live = sources.filter((s) => !s.removedAt)
  const items: AnalyzeItem[] = live.map((s) => ({
    key: s.alias,
    alias: s.alias,
    label: `Analyze ${s.alias}`,
    detail: s.root ? `${s.repository}/${s.root}` : s.repository,
    search: `analyze ${s.alias} ${s.repository} ${s.root}`.trim(),
  }))
  if (live.length < 2) return items
  return [
    ...items,
    {
      key: "*",
      label: "Analyze all repositories",
      detail: `${live.length} repositories`,
      search: "analyze all repositories",
    },
  ]
}
