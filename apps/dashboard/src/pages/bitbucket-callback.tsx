import {
  useBitbucketConnect,
  useBitbucketRepositories,
  useBitbucketStatus,
} from "@/client/forge/loomplane/hooks"
import { useInvalidate, useMutation } from "@/client/hooks"
import { BitbucketMark } from "@/components/bitbucket-mark"
import { HostCallback } from "@/components/host-callback"

export interface BitbucketCallbackProps {
  code: string
  state: string
  /** What Bitbucket sent when the person refused or it failed. */
  error: string
}

/**
 * BitbucketCallback is where Bitbucket sends someone back after they
 * approve Loomplane's consumer. It hands the code and state to the API,
 * which checks that this person began the sign-in here, then goes back to
 * Integrations.
 */
export function BitbucketCallback({
  code,
  state,
  error,
}: BitbucketCallbackProps) {
  const connect = useMutation(useBitbucketConnect)
  const invalidate = useInvalidate()
  return (
    <HostCallback
      name="Bitbucket"
      mark={<BitbucketMark />}
      code={code}
      state={state}
      error={error}
      connect={connect}
      onConnected={() => {
        invalidate(useBitbucketStatus)
        invalidate(useBitbucketRepositories)
      }}
    />
  )
}
