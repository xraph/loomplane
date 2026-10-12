const maxSlug = 48

/**
 * slugify mirrors the API's Slugify (internal/projects): lower case letters,
 * digits and single hyphens, at most 48 characters. The API decides; this
 * only previews what it will choose.
 */
export function slugify(name: string): string {
  let out = ""
  let hyphen = false
  for (const ch of name.toLowerCase()) {
    if ((ch >= "a" && ch <= "z") || (ch >= "0" && ch <= "9")) {
      out += ch
      hyphen = false
    } else if (out.length > 0 && !hyphen) {
      out += "-"
      hyphen = true
    }
  }
  out = out.replace(/-+$/, "")
  if (out.length > maxSlug) out = out.slice(0, maxSlug).replace(/-+$/, "")
  return out
}
