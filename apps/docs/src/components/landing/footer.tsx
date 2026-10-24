import Link from "next/link";
import { installGuideHref } from "./install-commands";
import { githubUrl } from "./site";
import { Wordmark } from "./wordmark";

const columns = [
  {
    title: "Platform",
    links: [
      { href: "/platform", text: "Discovery and plans" },
      { href: "/clouds", text: "Clouds" },
      { href: "/services", text: "Databases" },
      { href: "/security", text: "Security" },
    ],
  },
  {
    title: "Build with",
    links: [
      { href: "/agents", text: "loomctl" },
      { href: "/agents", text: "MCP for agents" },
      { href: "/clouds", text: "Provider plugins" },
      { href: "/self-host", text: "Self-host" },
    ],
  },
  {
    title: "Docs",
    links: [
      { href: "/docs", text: "Documentation" },
      { href: installGuideHref, text: "Quick start" },
    ],
  },
  {
    title: "Project",
    links: [{ href: githubUrl, text: "GitHub" }],
  },
];

export function Footer() {
  return (
    <footer>
      <div className="wrap cols">
        <div>
          <Wordmark className="foot-mark" />
          <p className="foot-note">
            A self-hosted deployment platform for the monorepos you already
            have. Built with Go, React and the Forge ecosystem.
          </p>
        </div>
        {columns.map((c) => (
          <div key={c.title}>
            <h5>{c.title}</h5>
            <ul>
              {c.links.map((l) => (
                <li key={l.text}>
                  {l.href.startsWith("http") ? (
                    <a href={l.href}>{l.text}</a>
                  ) : (
                    <Link href={l.href}>{l.text}</Link>
                  )}
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
      <div className="legal">
        <div className="wrap">
          <span>© 2026 Loomplane</span>
          <span>Open source and self-hosted</span>
        </div>
      </div>
    </footer>
  );
}
