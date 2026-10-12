/** statusOf is the HTTP status of a failed generated-client call, or 0. */
export function statusOf(err: unknown): number {
  if (err && typeof err === "object" && "statusCode" in err) {
    const code = (err as { statusCode: unknown }).statusCode
    return typeof code === "number" ? code : 0
  }
  return 0
}

/**
 * messageOf turns a failure into a sentence a person can act on: the
 * server's own explanation when there is one.
 */
export function messageOf(err: unknown, fallback: string): string {
  const status = statusOf(err)
  const raw = err instanceof Error ? err.message : ""
  if (raw && !/^HTTP \d+/.test(raw)) return sentence(raw)
  if (status >= 500)
    return "Loomplane couldn't finish that. Try again in a moment."
  return fallback
}

function sentence(s: string): string {
  const t = s.charAt(0).toUpperCase() + s.slice(1)
  return /[.!?]$/.test(t) ? t : `${t}.`
}
