import { useState } from "react"

import { messageOf } from "@/client/errors"
import { useSetupEndCloudSignInOffer } from "@/client/forge/loomplane/hooks"
import { useMutation } from "@/client/hooks"
import { CloudSignInApps } from "@/components/cloud-sign-in-apps"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { setInstance, useInstance } from "@/lib/instance"
import { useNavigate } from "@/lib/router"

/**
 * CloudSignInStep is the optional last step of first-run setup: the first
 * platform admin can add the OAuth apps people sign in to clouds with, or
 * skip. Either button ends the offer, so the step shows once.
 */
export function CloudSignInStep() {
  const instance = useInstance()
  const navigate = useNavigate()
  const end = useMutation(useSetupEndCloudSignInOffer)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState("")

  const finish = async () => {
    setError("")
    try {
      await end.mutateAsync({})
    } catch (err) {
      setError(messageOf(err, "Setup couldn't be finished."))
      return
    }
    setInstance({ ...instance, cloudSignInOffer: false })
    void navigate({ to: "/" })
  }

  return (
    <div className="grid gap-5">
      <div className="grid gap-1.5">
        <h2 className="text-lg font-semibold">Cloud sign-in (optional)</h2>
        <p className="text-sm text-muted-foreground">
          Let people connect a cloud by signing in instead of pasting a token.
          Every organization on this instance uses these apps. You can change
          them later in Settings, under Cloud sign-in.
        </p>
      </div>
      <CloudSignInApps variant="setup" onSaved={() => setSaved(true)} />
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <div className="flex justify-end">
        <Button
          variant={saved ? "default" : "outline"}
          disabled={end.isPending}
          onClick={() => void finish()}
        >
          {end.isPending && <Spinner />}
          {saved ? "Continue" : "Skip for now"}
        </Button>
      </div>
    </div>
  )
}
