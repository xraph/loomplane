import type { CSSProperties } from 'react';
import Link from 'next/link';
import { CopyButton } from './copy-button';
import { installCommands, installGuideHref } from './install-commands';

/** Two install commands beside one square button into the install guide. */
export function HeroCta() {
  return (
    <div className="cta-grid rise" style={{ '--d': '.28s' } as CSSProperties}>
      {installCommands.map((c) => (
        <div className="cta-row" key={c.id}>
          <span className="p">$</span>
          <code id={`hero-${c.id}`}>{c.text}</code>
          <span className="kind">{c.kind}</span>
          <CopyButton text={c.text} label={c.label} targetId={`hero-${c.id}`} />
        </div>
      ))}
      <Link className="cta-guide" href={installGuideHref} aria-label="Read the install guide">
        <span className="top">
          <svg
            width="22"
            height="22"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
          >
            <path d="M4 5.5A2.5 2.5 0 0 1 6.5 3H20v15H6.5A2.5 2.5 0 0 0 4 20.5z" />
            <path d="M4 20.5A2.5 2.5 0 0 0 6.5 23H20v-5" />
            <path d="M9 8h7M9 11.5h5" />
          </svg>
          <span className="arr" aria-hidden="true">
            ↗
          </span>
        </span>
        <span>Install guide</span>
      </Link>
    </div>
  );
}
