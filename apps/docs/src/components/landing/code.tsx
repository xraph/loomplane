import type { CSSProperties } from "react";

/**
 * A code block from the landing pages. `html` is trusted, first-party markup
 * with syntax spans; each line becomes its own element so editors can number
 * them and terminals can type them in.
 */
export function Code({ html }: { html: string }) {
  const lines = html.split("\n");
  return (
    <pre>
      {lines.map((line, i) => (
        <span
          // the lines are static and never reorder
          // biome-ignore lint/suspicious/noArrayIndexKey: static lines
          key={i}
          className="ln"
          style={{ "--i": i } as CSSProperties}
          // biome-ignore lint/security/noDangerouslySetInnerHtml: trusted first-party markup
          dangerouslySetInnerHTML={{ __html: line }}
        />
      ))}
    </pre>
  );
}
