import {
  useGitlabConnect,
  useGitlabRepositories,
  useGitlabStatus,
} from "@/client/forge/loomplane/hooks"
import { useInvalidate, useMutation } from "@/client/hooks"
import { GitLabMark } from "@/components/gitlab-mark"
import { HostCallback } from "@/components/host-callback"

export interface GitLabCallbackProps {
  code: string
  state: string
  /** What GitLab sent when the person refused or it failed. */
  error: string
}

/**
 * GitLabCallback is where GitLab sends someone back after they sign in. It
 * hands the code and state to the API, which checks that this person began
 * the sign-in here, then goes back to Integrations.
 */
export function GitLabCallback({ code, state, error }: GitLabCallbackProps) {
  const connect = useMutation(useGitlabConnect)
  const invalidate = useInvalidate()
  return (
    <HostCallback
      name="GitLab"
      mark={<GitLabMark />}
      code={code}
      state={state}
      error={error}
      connect={connect}
      onConnected={() => {
        invalidate(useGitlabStatus)
        invalidate(useGitlabRepositories)
      }}
    />
  )
}
