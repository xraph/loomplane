import { createFileRoute } from "@tanstack/react-router"

import { NewFromRepositoryPage } from "@/pages/new-from-repository"

type Search = {
  /** repo is the repository a "Deploy with Loomplane" button names. */
  repo?: string
}

export const Route = createFileRoute("/new")({
  validateSearch: (search: Record<string, unknown>): Search => ({
    repo:
      typeof search.repo === "string" && search.repo ? search.repo : undefined,
  }),
  component: NewRoute,
})

function NewRoute() {
  const { repo } = Route.useSearch()
  return <NewFromRepositoryPage repo={repo} />
}
