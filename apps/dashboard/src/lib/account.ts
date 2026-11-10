// Your own account: profile, password, sessions. These calls go through
// authsome's SDK, which owns the session; the generated client only covers
// organizations (scripts/authsome-spec.mjs says why).
import {
  type PasskeyRegisterFinishRequest,
  prepareCreationOptions,
  serializeCredential,
} from "@authsome/ui-core"
import { useAuth, useSessionToken } from "@authsome/ui-react"
import { useCallback, useEffect, useRef, useState } from "react"

import { messageOf } from "@/client/errors"

/**
 * useAccountClient returns authsome's client and the token its calls take.
 * The dashboard signs in with a cookie, so the token is usually empty and
 * the cookie authenticates the call.
 */
export function useAccountClient() {
  const { client } = useAuth()
  const token = useSessionToken() ?? ""
  return { client, token }
}

export type Load<T> =
  | { status: "loading"; data?: undefined; error?: undefined }
  | { status: "error"; data?: undefined; error: unknown }
  | { status: "success"; data: T; error?: undefined }

/**
 * useLoad runs an SDK read when the page opens and again on reload. A reload
 * keeps showing what's there until the new answer arrives.
 */
export function useLoad<T>(read: () => Promise<T>): Load<T> & {
  reload: () => Promise<void>
} {
  const [state, setState] = useState<Load<T>>({ status: "loading" })
  const latest = useRef(read)
  useEffect(() => {
    latest.current = read
  })
  const reload = useCallback(async () => {
    try {
      const data = await latest.current()
      setState({ status: "success", data })
    } catch (error) {
      setState({ status: "error", error })
    }
  }, [])
  useEffect(() => {
    void reload()
  }, [reload])
  return { ...state, reload }
}

/** A session as authsome lists it: only the fields the page shows. */
export type AccountSession = {
  id: string
  ipAddress: string
  userAgent: string
  createdAt: string
  lastActivityAt: string
}

/** toSession reads one row of authsome's session list. */
export function toSession(row: Record<string, unknown>): AccountSession {
  const text = (key: string) =>
    typeof row[key] === "string" ? (row[key] as string) : ""
  return {
    id: text("id"),
    ipAddress: text("ip_address"),
    userAgent: text("user_agent"),
    createdAt: text("created_at"),
    lastActivityAt: text("last_activity_at"),
  }
}

/**
 * describeAgent names a browser from its user agent, like "Chrome on
 * macOS". Order matters: Edge and Opera say Chrome too, and Chrome says
 * Safari.
 */
export function describeAgent(agent: string): {
  label: string
  phone: boolean
} {
  if (!agent) return { label: "Unknown device", phone: false }
  if (/loomctl|loomplane-cli/i.test(agent))
    return { label: "Loomplane CLI", phone: false }
  if (/^Dart\/|Flutter|Loomplane\/.*(iOS|Android)/i.test(agent))
    return { label: "Loomplane mobile app", phone: true }
  const browser = /Edg\//.test(agent)
    ? "Edge"
    : /OPR\//.test(agent)
      ? "Opera"
      : /Firefox\//.test(agent)
        ? "Firefox"
        : /Chrome\/|CriOS\//.test(agent)
          ? "Chrome"
          : /Safari\//.test(agent)
            ? "Safari"
            : "A browser"
  const system = /iPhone/.test(agent)
    ? "iPhone"
    : /iPad/.test(agent)
      ? "iPad"
      : /Android/.test(agent)
        ? "Android"
        : /Mac OS X|Macintosh/.test(agent)
          ? "macOS"
          : /Windows/.test(agent)
            ? "Windows"
            : /Linux/.test(agent)
              ? "Linux"
              : ""
  return {
    label: system ? `${browser} on ${system}` : browser,
    phone: /iPhone|iPad|Android/.test(agent),
  }
}

/**
 * factorProblem says what went wrong changing how you sign in, in plain
 * words for the messages authsome sends back, and otherwise falls back to
 * messageOf's reading.
 */
export function factorProblem(err: unknown, fallback: string): string {
  const raw = err instanceof Error ? err.message : ""
  if (/recent sign-in required/i.test(raw))
    return "For your safety, this needs a fresh sign-in. Sign out, sign back in, then set it up within five minutes."
  if (/already used/i.test(raw))
    return "That code was just used. Wait for your app to show the next one."
  if (/invalid (totp )?code|code required/i.test(raw))
    return "That code didn't work. Check your app and try the newest one."
  return messageOf(err, fallback)
}

/** sixDigits reports whether a code looks like an authenticator app's. */
export function sixDigits(code: string): boolean {
  return /^\d{6}$/.test(code)
}

/** passkeysWork reports whether this browser can make and use passkeys. */
export function passkeysWork(): boolean {
  return (
    typeof window !== "undefined" &&
    "PublicKeyCredential" in window &&
    typeof navigator !== "undefined" &&
    navigator.credentials !== undefined
  )
}

/**
 * addPasskey runs the whole registration: authsome's challenge, the
 * browser's prompt (Touch ID, Windows Hello, a security key), then the
 * credential back to authsome. code is the two-step code, needed only when
 * two-step is on.
 */
export async function addPasskey(
  client: ReturnType<typeof useAuth>["client"],
  token: string,
  name: string,
  code?: string
): Promise<void> {
  // The SDK's request type predates the code field; authsome reads it.
  const body = code ? { display_name: name, code } : { display_name: name }
  const { options } = await client.passkeyRegisterBegin(body, token)
  const credential = await navigator.credentials.create({
    publicKey: prepareCreationOptions(options),
  })
  if (!credential) throw new Error("No passkey was made.")
  await client.passkeyRegisterFinish(
    serializeCredential(credential) as unknown as PasskeyRegisterFinishRequest,
    token
  )
}
