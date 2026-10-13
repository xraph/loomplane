const maxAlias = 32

// reserved are the ecosystem names, which can't name a repository: an
// alias of "go" would make svc:go:go:cmd/api read two ways.
const reserved = new Set(["go", "rust", "node", "python", "jvm", "container"])

const aliasPattern = /^[a-z][a-z0-9-]{0,31}$/

/**
 * aliasFor mirrors the API's AliasFor (internal/discovery): the alias a
 * repository gets from its name when none is given. The API decides; this
 * only previews what it will choose.
 */
export function aliasFor(name: string): string {
  let out = ""
  let hyphen = false
  for (const ch of name.replace(/\.git$/, "").toLowerCase()) {
    if ((ch >= "a" && ch <= "z") || (ch >= "0" && ch <= "9")) {
      out += ch
      hyphen = false
    } else if (!hyphen) {
      out += "-"
      hyphen = true
    }
  }
  out = out.replace(/^-+|-+$/g, "")
  if (out === "") out = "repo"
  else if (out[0] >= "0" && out[0] <= "9") out = "r-" + out
  if (out.length > maxAlias) out = out.slice(0, maxAlias).replace(/-+$/, "")
  if (reserved.has(out)) out += "-src"
  return out
}

/**
 * aliasProblem says why an alias someone typed can't name a repository, or
 * returns an empty string, the way the API's ValidAlias does.
 */
export function aliasProblem(alias: string): string {
  if (reserved.has(alias)) return "That's an ecosystem name. Pick another."
  if (!aliasPattern.test(alias))
    return "Use up to 32 lower case letters, digits and hyphens, starting with a letter."
  return ""
}
