"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import { installGuideHref } from "./install-commands";
import { githubUrl, links } from "./site";
import { Wordmark } from "./wordmark";

export function Nav() {
  const pathname = usePathname();
  const [scrolled, setScrolled] = useState(false);
  const [open, setOpen] = useState(false);

  useEffect(() => {
    const onScroll = () => setScrolled(window.scrollY > 4);
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);

  // a new page starts with the menu closed
  // biome-ignore lint/correctness/useExhaustiveDependencies: runs on navigation
  useEffect(() => setOpen(false), [pathname]);

  const current = (href: string) => (pathname === href ? "page" : undefined);

  return (
    <header className={scrolled ? "nav scrolled" : "nav"}>
      <div className="wrap">
        <Link className="brand" href="/" aria-label="Loomplane home">
          <Wordmark />
        </Link>
        <nav className="nav-links" aria-label="Primary">
          {links.map((l) => (
            <Link key={l.href} href={l.href} aria-current={current(l.href)}>
              {l.text}
            </Link>
          ))}
        </nav>
        <div className="nav-right">
          <a className="gh" href={githubUrl} aria-label="Loomplane on GitHub">
            <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
              <path
                fill="currentColor"
                d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.470 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"
              />
            </svg>
            <span>Star on GitHub</span>
          </a>
          <Link className="btn" href="/docs">
            Docs
          </Link>
          <Link className="btn btn-blue" href={installGuideHref}>
            Get started
          </Link>
          <button
            className="menu-btn"
            type="button"
            aria-expanded={open}
            aria-controls="mobile-menu"
            onClick={() => setOpen((o) => !o)}
          >
            Menu
          </button>
        </div>
      </div>
      <div className="mobile-menu" id="mobile-menu" hidden={!open}>
        <div className="wrap">
          <Link href="/" aria-current={current("/")}>
            Home
          </Link>
          {links.map((l) => (
            <Link key={l.href} href={l.href} aria-current={current(l.href)}>
              {l.text}
            </Link>
          ))}
          <Link href="/docs">Docs</Link>
        </div>
      </div>
    </header>
  );
}
