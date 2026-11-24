// The "Deploy with Loomplane" button a repository's README can carry. It
// links to /new on this Loomplane, which opens New project with the
// repository filled in.
import { repositoryKey, repositoryURL } from "@/lib/format"

/** The button's image, served by the docs site for every Loomplane. */
export const deployButtonImage =
  "https://www.loomplane.com/deploy-button-dark.svg"

/**
 * deployButtonLink is where the button goes: /new on the Loomplane at
 * origin, naming the repository by its web URL. The URL stays readable in
 * the README; /new reads it unencoded.
 */
export function deployButtonLink(origin: string, repository: string): string {
  const key = repositoryKey(repository)
  return `${origin.replace(/\/+$/, "")}/new?repo=${key ? repositoryURL(key) : encodeURIComponent(repository.trim())}`
}

/** deployButtonMarkdown is the button as a README pastes it. */
export function deployButtonMarkdown(
  origin: string,
  repository: string
): string {
  return `[![Deploy with Loomplane](${deployButtonImage})](${deployButtonLink(origin, repository)})`
}
