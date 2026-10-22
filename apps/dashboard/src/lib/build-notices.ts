// Builds whose output isn't streamed, folded into one note per reason.

export type Collection = {
  id: string
  actionId: string
  state: string
  message: string
}

/**
 * notices folds the builds whose output isn't streamed into one line per
 * reason, so five App Platform builds read as one note rather than five.
 */
export function notices(collections: Collection[], step?: string | null) {
  const out = new Map<
    string,
    { key: string; unavailable: boolean; message: string; count: number }
  >()
  for (const c of collections) {
    if (c.state === "complete" || c.state === "collecting") continue
    if (step && c.actionId !== step) continue
    const unavailable = c.state === "unavailable"
    const key = `${unavailable}:${c.message}`
    const seen = out.get(key)
    if (seen) seen.count++
    else out.set(key, { key, unavailable, message: c.message, count: 1 })
  }
  return [...out.values()]
}
