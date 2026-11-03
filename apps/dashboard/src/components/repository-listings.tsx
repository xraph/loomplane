// The repositories each connected host lets Loomplane read, asked for in turn.
// The repository field and the Integrations summary both list them.
import type { ReactNode } from "react"

import {
  useBitbucketRepositories,
  useGithubRepositories,
  useGitlabRepositories,
} from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import {
  fromBitbucket,
  fromGitHub,
  fromGitLab,
  type PickerRepository,
} from "@/lib/integrations"

/** What one host's list of repositories is doing. */
export type Listing = {
  /** The host's name, for saying which one didn't answer. */
  name: string
  repositories: PickerRepository[]
  loading: boolean
  error?: unknown
  retry?: () => Promise<unknown>
}

type ListingProps = { children: (l: Listing) => ReactNode }

function GithubListing({ children }: ListingProps) {
  const repos = useQuery(useGithubRepositories)
  return children({
    name: "GitHub",
    repositories: (repos.data?.repositories ?? []).map(fromGitHub),
    loading: !repos.data && repos.status !== "error",
    error: repos.status === "error" ? repos.error : undefined,
    retry: repos.refetch,
  })
}

function GitlabListing({ children }: ListingProps) {
  const repos = useQuery(useGitlabRepositories)
  return children({
    name: "GitLab",
    repositories: (repos.data?.repositories ?? []).map(fromGitLab),
    loading: !repos.data && repos.status !== "error",
    error: repos.status === "error" ? repos.error : undefined,
    retry: repos.refetch,
  })
}

function BitbucketListing({ children }: ListingProps) {
  const repos = useQuery(useBitbucketRepositories)
  return children({
    name: "Bitbucket",
    repositories: (repos.data?.repositories ?? []).map(fromBitbucket),
    loading: !repos.data && repos.status !== "error",
    error: repos.status === "error" ? repos.error : undefined,
    retry: repos.refetch,
  })
}

function Chain({
  hosts,
  taken,
  children,
}: {
  hosts: ((p: ListingProps) => ReactNode)[]
  taken: Listing[]
  children: (listings: Listing[]) => ReactNode
}) {
  const [Host, ...rest] = hosts
  if (!Host) return children(taken)
  return (
    <Host>
      {(l) => (
        <Chain hosts={rest} taken={[...taken, l]}>
          {children}
        </Chain>
      )}
    </Host>
  )
}

/**
 * RepositoryListings asks each host named for its repositories and hands
 * all the lists on together, in the order GitHub, GitLab, Bitbucket. The
 * lists ask the hosts, and a hook can't be skipped, so a host's is only
 * asked for once an account is connected there.
 */
export function RepositoryListings({
  github,
  gitlab,
  bitbucket,
  children,
}: {
  github?: boolean
  gitlab?: boolean
  bitbucket?: boolean
  children: (listings: Listing[]) => ReactNode
}) {
  const hosts = [
    github && GithubListing,
    gitlab && GitlabListing,
    bitbucket && BitbucketListing,
  ].filter((h): h is typeof GithubListing => !!h)
  return (
    <Chain hosts={hosts} taken={[]}>
      {children}
    </Chain>
  )
}
