// Random values for keys, secrets and passwords, made in the browser with
// the Web Crypto API: the same randomness `openssl rand` reads, so a value
// never leaves the page until it's set.

export interface Generator {
  id: string
  label: string
  /** How else you would make it, such as the openssl command. */
  hint: string
  generate: () => string
}

function bytes(n: number): Uint8Array {
  const b = new Uint8Array(n)
  crypto.getRandomValues(b)
  return b
}

function hex(b: Uint8Array): string {
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("")
}

function base64(b: Uint8Array): string {
  let s = ""
  for (const x of b) s += String.fromCharCode(x)
  return btoa(s)
}

function base64url(b: Uint8Array, pad: boolean): string {
  const s = base64(b).replace(/\+/g, "-").replace(/\//g, "_")
  return pad ? s : s.replace(/=+$/, "")
}

const passwordAlphabet =
  "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

/**
 * password picks length characters from an alphabet without look-alikes
 * (0 and O, 1, l and I). Bytes that would bias the pick are drawn again.
 */
export function password(length = 24): string {
  const limit = 256 - (256 % passwordAlphabet.length)
  let out = ""
  while (out.length < length) {
    for (const x of bytes(length)) {
      if (x < limit && out.length < length) {
        out += passwordAlphabet[x % passwordAlphabet.length]
      }
    }
  }
  return out
}

export const generators: Generator[] = [
  {
    id: "hex32",
    label: "Hex, 32 bytes",
    hint: "openssl rand -hex 32",
    generate: () => hex(bytes(32)),
  },
  {
    id: "hex16",
    label: "Hex, 16 bytes",
    hint: "openssl rand -hex 16",
    generate: () => hex(bytes(16)),
  },
  {
    id: "base64-32",
    label: "Base64, 32 bytes",
    hint: "openssl rand -base64 32",
    generate: () => base64(bytes(32)),
  },
  {
    id: "base64-64",
    label: "Base64, 64 bytes",
    hint: "openssl rand -base64 64",
    generate: () => base64(bytes(64)),
  },
  {
    id: "base64url-32",
    label: "URL-safe Base64, 32 bytes",
    hint: "Fernet and JWT keys",
    generate: () => base64url(bytes(32), true),
  },
  {
    id: "uuid",
    label: "UUID",
    hint: "uuidgen",
    generate: () => crypto.randomUUID(),
  },
  {
    id: "password",
    label: "Password, 24 characters",
    hint: "letters and digits, no look-alikes",
    generate: () => password(24),
  },
]

/** generatorFor picks the generator a key most likely wants. */
export function generatorFor(key: string): Generator {
  const k = key.toUpperCase()
  const id = /PASSWORD|PASSWD|_PASS$/.test(k)
    ? "password"
    : /UUID/.test(k)
      ? "uuid"
      : "hex32"
  return generators.find((g) => g.id === id) as Generator
}
