// A fenced block in the agent's answer: its language and a copy button
// on top, the code below, coloured once the highlighter has loaded. Until
// then, and for a language it doesn't offer, the code shows as plain text.
import { useEffect, useState } from "react"

import { CopyButton } from "@/components/runs"
import { type TokenLine, grammarOf, tokens } from "@/lib/highlight"
import { cn } from "@/lib/utils"

export function CodeBlock({
  code,
  lang,
  className,
}: {
  code: string
  /** The fence's language, as the agent wrote it. */
  lang?: string
  className?: string
}) {
  const text = code.replace(/\n$/, "")
  const grammar = grammarOf(lang)
  const [lit, setLit] = useState<{ text: string; lines: TokenLine[] }>()

  useEffect(() => {
    if (!grammar) return
    let live = true
    void tokens(text, grammar).then((lines) => {
      if (live && lines) setLit({ text, lines })
    })
    return () => {
      live = false
    }
  }, [text, grammar])

  // While the agent is still writing, the last colouring stays up until
  // the next one is ready, unless the code it coloured is gone.
  const lines = lit && text.startsWith(lit.text) ? lit.lines : undefined
  const rest = lines && lit ? text.slice(lit.text.length) : ""

  return (
    <div
      className={cn(
        "group/code my-3 max-w-full overflow-hidden rounded-lg bg-subtle ring-1 ring-border",
        className
      )}
    >
      <div className="flex h-8 items-center justify-between border-b pr-1 pl-3">
        <span className="font-mono text-[11px] text-muted-foreground lowercase">
          {lang?.trim() || "text"}
        </span>
        <CopyButton value={text} label="Copy code" />
      </div>
      <pre className="overflow-x-auto p-3 font-mono text-[12px] leading-relaxed">
        <code className="[&_span]:text-(--shiki-light) dark:[&_span]:text-(--shiki-dark)">
          {lines
            ? lines.map((line, i) => (
                <span key={i} className="block min-h-[1lh]">
                  {line.map((t, j) => (
                    <span key={j} style={t.htmlStyle}>
                      {t.content}
                    </span>
                  ))}
                </span>
              ))
            : text}
          {rest}
        </code>
      </pre>
    </div>
  )
}
