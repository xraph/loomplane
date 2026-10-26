// Date only validates the whole second. Fractional digits stay in integer
// nanoseconds so the two window edges cannot collapse into one millisecond.
function utcNanoseconds(value: string): bigint | undefined {
  const parts = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?Z$/.exec(
    value
  )
  if (!parts) return undefined
  const whole = `${parts[1]}Z`
  const milliseconds = Date.parse(whole)
  if (
    !Number.isFinite(milliseconds) ||
    new Date(milliseconds).toISOString().replace(".000Z", "Z") !== whole
  )
    return undefined
  return (
    BigInt(milliseconds) * 1_000_000n + BigInt((parts[2] ?? "").padEnd(9, "0"))
  )
}

export function coveredPITRTime(
  value: string,
  earliest: string | undefined,
  latest: string | undefined,
  provider: string
): boolean {
  if (!earliest || !latest || (provider !== "cloud-sql" && value.includes(".")))
    return false
  const time = utcNanoseconds(value)
  const first = utcNanoseconds(earliest)
  const last = utcNanoseconds(latest)
  return (
    time !== undefined &&
    first !== undefined &&
    last !== undefined &&
    time >= first &&
    time < last
  )
}
