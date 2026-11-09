// Reading pasted .env content into keys and values, in the browser: what
// someone pastes is never sent anywhere until they choose to set it.

import { keyValid } from "@/lib/variables"

export interface DotenvEntry {
  key: string
  value: string
  /** The 1-based line the key is on. */
  line: number
}

export interface DotenvProblem {
  line: number
  message: string
}

export interface Dotenv {
  entries: DotenvEntry[]
  problems: DotenvProblem[]
}

const assignment = /^(?:export\s+)?([^=\s]+)\s*=(.*)$/

const escapes: Record<string, string> = {
  n: "\n",
  r: "\r",
  t: "\t",
  '"': '"',
  "\\": "\\",
}

/**
 * parseDotenv reads KEY=value lines the way dotenv does: `export` is
 * allowed, # starts a comment, a double-quoted value may span lines and
 * takes \n escapes, a single-quoted one is taken as it is. A key set twice
 * keeps its later value.
 */
export function parseDotenv(text: string): Dotenv {
  const lines = text.replace(/^\uFEFF/, "").split(/\r?\n/)
  const byKey = new Map<string, DotenvEntry>()
  const problems: DotenvProblem[] = []

  for (let i = 0; i < lines.length; i++) {
    const line = i + 1
    const raw = lines[i].trim()
    if (raw === "" || raw.startsWith("#")) continue
    const m = assignment.exec(raw)
    if (!m) {
      problems.push({
        line,
        message: "Not a KEY=value line, so it was left out.",
      })
      continue
    }
    const key = m[1]
    if (!keyValid(key)) {
      problems.push({
        line,
        message: `${key} isn't a key Loomplane can set: use letters, digits and underscores, not starting with a digit.`,
      })
      continue
    }
    let rest = m[2].trimStart()
    let value: string
    const quote = rest[0]
    if (quote === '"' || quote === "'" || quote === "`") {
      // Read on, across lines, to the closing quote.
      let body = rest.slice(1)
      let end = closing(body, quote)
      while (end < 0 && i + 1 < lines.length) {
        i++
        body += "\n" + lines[i]
        end = closing(body, quote)
      }
      if (end < 0) {
        problems.push({
          line,
          message: `${key}'s value has no closing ${quote}, so it runs to the end.`,
        })
        end = body.length
      }
      value = body.slice(0, end)
      if (quote === '"') {
        value = value.replace(/\\([nrt"\\])/g, (_, c: string) => escapes[c])
      }
    } else {
      const hash = rest.search(/\s#/)
      if (hash >= 0) rest = rest.slice(0, hash)
      value = rest.trim()
    }
    if (byKey.has(key)) {
      problems.push({
        line,
        message: `${key} is set more than once; the last value is kept.`,
      })
      byKey.delete(key)
    }
    byKey.set(key, { key, value, line })
  }
  return { entries: [...byKey.values()], problems }
}

/** closing is the index of the first unescaped quote in s, or -1. */
function closing(s: string, quote: string): number {
  for (let i = 0; i < s.length; i++) {
    if (s[i] === "\\" && quote === '"') {
      i++
      continue
    }
    if (s[i] === quote) return i
  }
  return -1
}
