// Code in the agent's answers, coloured. Shiki loads with the first code
// block, in a chunk of its own, on the JavaScript regex engine (no wasm),
// and each language loads the first time an answer uses it. Only the
// languages an infrastructure conversation is likely to show are offered;
// anything else stays plain text.
//
// tokens returns spans, not HTML, so nothing the agent wrote is ever set
// as markup: the block renders them as React text.
import type { HighlighterCore, ThemedToken } from "shiki/core"

const grammars = {
  bash: () => import("shiki/langs/bash.mjs"),
  diff: () => import("shiki/langs/diff.mjs"),
  dockerfile: () => import("shiki/langs/dockerfile.mjs"),
  go: () => import("shiki/langs/go.mjs"),
  hcl: () => import("shiki/langs/hcl.mjs"),
  ini: () => import("shiki/langs/ini.mjs"),
  javascript: () => import("shiki/langs/javascript.mjs"),
  json: () => import("shiki/langs/json.mjs"),
  log: () => import("shiki/langs/log.mjs"),
  python: () => import("shiki/langs/python.mjs"),
  sql: () => import("shiki/langs/sql.mjs"),
  toml: () => import("shiki/langs/toml.mjs"),
  tsx: () => import("shiki/langs/tsx.mjs"),
  typescript: () => import("shiki/langs/typescript.mjs"),
  yaml: () => import("shiki/langs/yaml.mjs"),
} as const

export type Grammar = keyof typeof grammars

const aliases: Record<string, Grammar> = {
  sh: "bash",
  shell: "bash",
  shellscript: "bash",
  zsh: "bash",
  console: "bash",
  docker: "dockerfile",
  golang: "go",
  terraform: "hcl",
  tf: "hcl",
  env: "ini",
  dotenv: "ini",
  js: "javascript",
  jsx: "tsx",
  ts: "typescript",
  jsonc: "json",
  py: "python",
  yml: "yaml",
  patch: "diff",
}

/** grammarOf names the grammar for a fence's language, if one is offered. */
export function grammarOf(lang: string | undefined): Grammar | undefined {
  const name = lang?.trim().toLowerCase()
  if (!name) return undefined
  if (Object.hasOwn(grammars, name)) return name as Grammar
  return aliases[name]
}

let highlighter: Promise<HighlighterCore> | undefined

function load(): Promise<HighlighterCore> {
  highlighter ??= (async () => {
    const [{ createHighlighterCore }, { createJavaScriptRegexEngine }] =
      await Promise.all([
        import("shiki/core"),
        import("shiki/engine/javascript"),
      ])
    return createHighlighterCore({
      themes: [
        import("shiki/themes/github-light.mjs"),
        import("shiki/themes/github-dark.mjs"),
      ],
      langs: [],
      engine: createJavaScriptRegexEngine(),
    })
  })()
  // A failed load (offline, a stale chunk) is tried again next time.
  highlighter.catch(() => {
    highlighter = undefined
  })
  return highlighter
}

/** A line of tokens; each token's style holds --shiki-light and
 * --shiki-dark, and the block picks one by theme. */
export type TokenLine = Pick<ThemedToken, "content" | "htmlStyle">[]

/** tokens colours code in a grammar, or says it couldn't. */
export async function tokens(
  code: string,
  grammar: Grammar
): Promise<TokenLine[] | undefined> {
  try {
    const h = await load()
    if (!h.getLoadedLanguages().includes(grammar))
      await h.loadLanguage(grammars[grammar]())
    return h.codeToTokens(code, {
      lang: grammar,
      themes: { light: "github-light", dark: "github-dark" },
      defaultColor: false,
    }).tokens
  } catch {
    return undefined
  }
}
