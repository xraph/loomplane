// The workspace in the dashboard's URLs.
//
// Every signed-in page sits under its workspace's slug: /acme-inc/projects
// rather than /projects. Routes and links stay unprefixed. The router's
// rewrite adds the slug on the way out and strips it on the way in, so no
// link can forget it, and a route written for one workspace works for all.
//
// The slug is the authsome organization's. Organization IDs would never
// collide with a page name, but nobody can read or type them.
import type { LocationRewrite } from "@tanstack/react-router"

// Pages that keep one address whoever is signed in: authsome's emails and
// the providers' callbacks link to them before any workspace is known.
const unprefixed = new Set([
  "/sign-in",
  "/sign-up",
  "/forgot-password",
  "/reset-password",
  "/verify-email",
  "/invitations/accept",
  "/integrations/oauth/callback",
  "/integrations/github/setup",
])

let current = ""

/** setWorkspaceSlug names the workspace every link from now on points into. */
export function setWorkspaceSlug(slug: string) {
  current = slug
}

export function isUnprefixed(pathname: string): boolean {
  return unprefixed.has(pathname.replace(/\/+$/, "") || "/")
}

/**
 * splitWorkspace separates a public pathname into the workspace it names and
 * the route path under it. A first segment that is also a top-level page,
 * such as /projects/abc, is an address from before workspaces were in the
 * URL, so it names none; the current workspace's slug always wins.
 */
export function splitWorkspace(
  pathname: string,
  pages: ReadonlySet<string>
): { workspace: string | null; path: string } {
  if (isUnprefixed(pathname)) return { workspace: null, path: pathname }
  const [, first = "", ...rest] = pathname.split("/")
  if (!first || (first !== current && pages.has(first)))
    return { workspace: null, path: pathname }
  return { workspace: decodeURIComponent(first), path: `/${rest.join("/")}` }
}

/** withWorkspace puts a route path under a workspace's slug. */
export function withWorkspace(slug: string, path: string): string {
  if (!slug || isUnprefixed(path)) return path
  return `/${encodeURIComponent(slug)}${path === "/" ? "" : path}`
}

/**
 * workspaceRewrite is the router's rewrite. pages holds the first segment of
 * every top-level route, which tells an old address from a workspace's.
 */
export function workspaceRewrite(pages: ReadonlySet<string>): LocationRewrite {
  return {
    input: ({ url }) => {
      const { workspace, path } = splitWorkspace(url.pathname, pages)
      if (workspace === null) return undefined
      url.pathname = path
      return url
    },
    output: ({ url }) => {
      if (!current) return undefined
      url.pathname = withWorkspace(current, url.pathname)
      return url
    },
  }
}

/**
 * requestedWorkspace is the slug a parsed location's address names, or null
 * when the address has none (an old link, or a page outside workspaces).
 */
export function requestedWorkspace(location: {
  publicHref: string
  pathname: string
}): string | null {
  const shown = new URL(location.publicHref, "http://loomplane.invalid")
  const pathname = decodeURI(shown.pathname)
  if (pathname === location.pathname) return null
  const first = shown.pathname.split("/")[1]
  return first ? decodeURIComponent(first) : null
}
