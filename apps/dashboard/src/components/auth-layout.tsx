import type { ReactNode } from "react"

import { ModeToggle } from "@/components/mode-toggle"

import "./auth-layout.css"

/** Shared frame for account access and the first organization setup. */
export function AuthLayout({
  title,
  children,
}: {
  title: string
  children: ReactNode
}) {
  return (
    <main className="auth-layout">
      <h1 className="sr-only">{title}</h1>
      <div className="auth-watermark" aria-hidden="true">
        <WovenThreads />
      </div>
      <section className="auth-access" aria-label="Account access">
        <header className="auth-toolbar">
          <ModeToggle />
        </header>
        <div className="auth-content">{children}</div>
        <footer className="auth-footer">Loomplane by Xraph</footer>
      </section>
    </main>
  )
}

/** Two interlaced bands, drawn from the brand's rounded L-shaped threads. */
function WovenThreads() {
  const threads = Array.from({ length: 8 }, (_, i) => i)

  return (
    <svg
      className="auth-weave"
      viewBox="0 0 640 480"
      fill="none"
      aria-hidden="true"
    >
      <defs>
        <mask id="auth-weave-under">
          <rect width="640" height="480" fill="white" />
          {threads.flatMap((row) =>
            threads.map((column) =>
              (row + column) % 2 === 0 ? (
                <rect
                  key={`top-${row}-${column}`}
                  x={116 + column * 18}
                  y={76 + row * 18}
                  width="12"
                  height="12"
                  rx="2"
                  fill="black"
                />
              ) : null
            )
          )}
          {threads
            .slice(0, 7)
            .flatMap((row) =>
              threads.map((column) =>
                (row + column) % 2 === 0 ? (
                  <rect
                    key={`bottom-${row}-${column}`}
                    x={560 - column * 18}
                    y={412 - row * 18}
                    width="12"
                    height="12"
                    rx="2"
                    fill="black"
                  />
                ) : null
              )
            )}
        </mask>
      </defs>

      <g
        stroke="var(--auth-thread-paper)"
        strokeWidth="7"
        strokeLinecap="round"
      >
        {threads.map((i) => (
          <path
            key={i}
            d={`M${122 + i * 18} 58V226Q${122 + i * 18} ${418 - i * 18} 314 ${418 - i * 18}H582`}
          />
        ))}
      </g>
      <g
        stroke="var(--auth-thread-gold)"
        strokeWidth="7"
        strokeLinecap="round"
        mask="url(#auth-weave-under)"
      >
        {threads.map((i) => (
          <path
            key={i}
            d={`M58 ${82 + i * 18}H344Q${566 - i * 18} ${82 + i * 18} ${566 - i * 18} 304V422`}
          />
        ))}
      </g>
    </svg>
  )
}
