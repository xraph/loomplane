import { useNavigate } from "@tanstack/react-router"
import type { MouseEvent, ReactNode } from "react"

import { AuthLogos } from "@/components/auth-logos"
import { AuthSilk } from "@/components/auth-silk"
import { BrandLogo } from "@/components/brand-logo"
import { ModeToggle } from "@/components/mode-toggle"
import { XraphMark } from "@/components/xraph-mark"

import "./auth-layout.css"

// The signed-out screens App renders inside this one frame.
const authPaths = new Set([
  "/sign-in",
  "/sign-up",
  "/forgot-password",
  "/reset-password",
  "/verify-email",
])

/**
 * Shared frame for account access and the first organization setup, in the
 * landing site's look: the logo top left, the form in a double-bezel card,
 * and the hero's light beside it. It follows the app's light and dark themes.
 *
 * Authsome's forms link between these screens with plain anchors, which would
 * reload the page and restart the frame. Clicks on those links route through
 * the app instead, so only the card changes.
 */
export function AuthLayout({
  title,
  children,
}: {
  title: string
  children: ReactNode
}) {
  const navigate = useNavigate()

  const routeAuthLinks = (event: MouseEvent<HTMLElement>) => {
    if (
      event.defaultPrevented ||
      event.button !== 0 ||
      event.metaKey ||
      event.ctrlKey ||
      event.shiftKey ||
      event.altKey
    ) {
      return
    }
    const link = (event.target as Element).closest?.("a[href]")
    if (!(link instanceof HTMLAnchorElement)) return
    if (
      (link.target && link.target !== "_self") ||
      link.hasAttribute("download")
    ) {
      return
    }
    const url = new URL(link.href, window.location.href)
    if (url.origin !== window.location.origin || !authPaths.has(url.pathname)) {
      return
    }
    event.preventDefault()
    void navigate({ href: url.pathname + url.search + url.hash })
  }

  return (
    <main className="auth-layout" onClickCapture={routeAuthLinks}>
      <h1 className="sr-only">{title}</h1>
      <section className="auth-access" aria-label="Account access">
        <header className="auth-toolbar">
          <BrandLogo className="w-34" />
          <ModeToggle />
        </header>
        <div className="auth-content">
          <div className="auth-bezel">
            <div className="auth-panel">{children}</div>
          </div>
        </div>
        <footer className="auth-footer">
          <a
            className="auth-by"
            href="https://xraph.com"
            target="_blank"
            rel="noreferrer"
          >
            Loomplane by
            <XraphMark className="auth-xraph" />
            <span>Xraph</span>
          </a>
          <span>Self-hosted, on your own clouds</span>
        </footer>
      </section>
      <aside className="auth-visual" aria-hidden="true">
        <AuthSilk />
        <AuthLogos />
        <div className="auth-visual-copy">
          <p className="auth-headline">
            Your infrastructure, <span>woven together.</span>
          </p>
          <ul className="auth-points">
            <li>Reads your code, never runs it</li>
            <li>Every change is a plan someone approves</li>
            <li>Runs in your own cloud accounts</li>
          </ul>
        </div>
      </aside>
    </main>
  )
}
