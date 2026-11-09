// Dollars as the billing routes send and read them. Money is whole cents
// (or micro-dollars for a rate) in memory and a decimal string on the wire,
// so no float ever holds an amount.

/** dollars writes cents as "$3.00". */
export function dollars(cents: number): string {
  const sign = cents < 0 ? "-" : ""
  const abs = Math.abs(cents)
  const whole = Math.floor(abs / 100)
  return `${sign}$${whole.toLocaleString("en-US")}.${String(abs % 100).padStart(2, "0")}`
}

/** wireDollars writes cents the way the routes read them: 2500 is "25.00". */
export function wireDollars(cents: number): string {
  return `${Math.floor(cents / 100)}.${String(cents % 100).padStart(2, "0")}`
}

/** parseUSD reads what a person typed as dollars: cents, or null for blank. */
export function parseUSD(
  text: string
): { cents: number | null } | { problem: string } {
  const t = text.trim().replace(/^\$/, "")
  if (t === "") return { cents: null }
  if (t.startsWith("-")) return { problem: "The limit can't be negative." }
  if (!/^\d+(\.\d*)?$/.test(t))
    return { problem: "Use a dollar amount, like 25 or 25.50." }
  const [whole, frac = ""] = t.split(".")
  if (frac.length > 2) return { problem: "Use at most two decimals." }
  const cents = Number(whole) * 100 + Number(frac.padEnd(2, "0"))
  if (!Number.isSafeInteger(cents))
    return { problem: "That amount is too large." }
  return { cents }
}

/**
 * parseRate reads a price per unit in dollars with at most six decimals,
 * and hands it back in the form the route reads.
 */
export function parseRate(text: string): { usd: string } | { problem: string } {
  const t = text.trim().replace(/^\$/, "")
  if (t === "") return { problem: "Enter a price, or 0 for free." }
  if (t.startsWith("-")) return { problem: "A price can't be negative." }
  if (!/^\d+(\.\d*)?$/.test(t))
    return { problem: "Use a dollar amount, like 0.012345." }
  const [whole, frac = ""] = t.split(".")
  if (frac.length > 6) return { problem: "Use at most six decimals." }
  if (Number(whole) > 1000 || (Number(whole) === 1000 && /[1-9]/.test(frac)))
    return { problem: "A price can be at most $1000 a unit." }
  return { usd: frac ? `${whole}.${frac}` : whole }
}

/** parseAmount reads dollars for a refund or a credit, at least a cent. */
export function parseAmount(
  text: string,
  max: number
): { cents: number } | { problem: string } {
  const r = parseUSD(text)
  if ("problem" in r) return r
  if (r.cents === null || r.cents <= 0)
    return { problem: "Enter at least $0.01." }
  if (r.cents > max) return { problem: `Enter at most ${dollars(max)}.` }
  return { cents: r.cents }
}

/** monthDay writes a UTC timestamp as "1 December". */
export function monthDay(iso: string): string {
  return new Date(iso).toLocaleDateString("en-GB", {
    day: "numeric",
    month: "long",
    timeZone: "UTC",
  })
}

/** shortDate writes a UTC timestamp as "1 Dec 2026". */
export function shortDate(iso: string): string {
  return new Date(iso).toLocaleDateString("en-GB", {
    day: "numeric",
    month: "short",
    year: "numeric",
    timeZone: "UTC",
  })
}
