/** statusOf is the HTTP status of a failed generated-client call, or 0. */
export function statusOf(err: unknown): number {
  if (err && typeof err === "object" && "statusCode" in err) {
    const code = (err as { statusCode: unknown }).statusCode
    return typeof code === "number" ? code : 0
  }
  return 0
}

const GENERIC_SERVER_STATUS =
  /^(?:HTTP \d+|Internal Server Error|Bad Gateway|Service Unavailable|Gateway Timeout)$/i

/**
 * messageOf turns a failure into a sentence a person can act on: the
 * server's own explanation when there is one.
 */
export function messageOf(
  err: unknown,
  fallback: string,
  serverFallback?: string
): string {
  const status = statusOf(err)
  const details =
    err && typeof err === "object" && "details" in err ? err.details : undefined
  if (
    details &&
    typeof details === "object" &&
    "detail" in details &&
    typeof details.detail === "string" &&
    details.detail.trim()
  ) {
    return sentence(details.detail.trim())
  }
  const raw = err instanceof Error ? err.message : ""
  if (
    raw &&
    !(status >= 500 && GENERIC_SERVER_STATUS.test(raw.trim())) &&
    !/^HTTP \d+/.test(raw)
  )
    return sentence(raw)
  if (status >= 500)
    return serverFallback
      ? `${serverFallback} (HTTP ${status}).`
      : "Loomplane couldn't finish that. Try again in a moment."
  return fallback
}

function sentence(s: string): string {
  const t = s.charAt(0).toUpperCase() + s.slice(1)
  return /[.!?]$/.test(t) ? t : `${t}.`
}
