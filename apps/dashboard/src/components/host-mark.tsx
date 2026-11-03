import { BitbucketMark } from "@/components/bitbucket-mark"
import { GitHubMark } from "@/components/github-mark"
import { GitLabMark } from "@/components/gitlab-mark"

/**
 * HostMark is the small mark of the host a repository lives on, named for
 * assistive technology by that host, so a GitHub acme and a Bitbucket acme
 * can't be confused.
 */
export function HostMark({
  provider,
  host,
  className,
}: {
  provider: "github" | "gitlab" | "bitbucket"
  host: string
  className?: string
}) {
  const Mark =
    provider === "bitbucket"
      ? BitbucketMark
      : provider === "gitlab"
        ? GitLabMark
        : GitHubMark
  return (
    <span role="img" aria-label={host} title={host} className="inline-flex">
      <Mark className={className} />
    </span>
  )
}
