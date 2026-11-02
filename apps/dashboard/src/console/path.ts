// Whether pathname is base or sits below it. "/__consoles" is a different
// path from "/__console", so the match is on whole segments.
export function isUnder(pathname: string, base: string): boolean {
  const root = base.replace(/\/+$/, "")
  return pathname === root || pathname.startsWith(`${root}/`)
}
