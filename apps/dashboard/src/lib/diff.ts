// A line diff, for comparing manifest revisions. Manifests are short, so
// the plain longest-common-subsequence table is fine.

export type DiffLine = { kind: "same" | "added" | "removed"; text: string }

/** diffLines is what turns before into after, line by line. */
export function diffLines(before: string, after: string): DiffLine[] {
  const a = split(before)
  const b = split(after)
  // lcs[i][j] is the longest common subsequence of a[i:] and b[j:].
  const lcs: number[][] = Array.from({ length: a.length + 1 }, () =>
    new Array<number>(b.length + 1).fill(0)
  )
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      lcs[i][j] =
        a[i] === b[j]
          ? lcs[i + 1][j + 1] + 1
          : Math.max(lcs[i + 1][j], lcs[i][j + 1])
    }
  }
  const out: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ kind: "same", text: a[i] })
      i++
      j++
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      out.push({ kind: "removed", text: a[i++] })
    } else {
      out.push({ kind: "added", text: b[j++] })
    }
  }
  while (i < a.length) out.push({ kind: "removed", text: a[i++] })
  while (j < b.length) out.push({ kind: "added", text: b[j++] })
  return out
}

/** changeCount counts the lines a diff adds and removes. */
export function changeCount(d: DiffLine[]): { added: number; removed: number } {
  return {
    added: d.filter((l) => l.kind === "added").length,
    removed: d.filter((l) => l.kind === "removed").length,
  }
}

function split(s: string): string[] {
  if (s === "") return []
  return s.replace(/\n$/, "").split("\n")
}

/** A hunk is either lines to show or a run of unchanged lines folded away. */
export type Hunk =
  { kind: "lines"; lines: DiffLine[] } | { kind: "folded"; count: number }

/**
 * hunks folds unchanged runs longer than twice the context, keeping context
 * lines on each side of a change. A diff with no changes isn't folded: it's
 * the first revision, and the whole thing is the point.
 */
export function hunks(d: DiffLine[], context = 3): Hunk[] {
  if (d.every((l) => l.kind === "same")) return [{ kind: "lines", lines: d }]
  const out: Hunk[] = []
  let shown: DiffLine[] = []
  let i = 0
  while (i < d.length) {
    if (d[i].kind !== "same") {
      shown.push(d[i++])
      continue
    }
    let end = i
    while (end < d.length && d[end].kind === "same") end++
    const head = i === 0 ? 0 : context
    const tail = end === d.length ? 0 : context
    if (end - i > head + tail) {
      shown.push(...d.slice(i, i + head))
      if (shown.length > 0) out.push({ kind: "lines", lines: shown })
      out.push({ kind: "folded", count: end - i - head - tail })
      shown = d.slice(end - tail, end)
    } else {
      shown.push(...d.slice(i, end))
    }
    i = end
  }
  if (shown.length > 0) out.push({ kind: "lines", lines: shown })
  return out
}
