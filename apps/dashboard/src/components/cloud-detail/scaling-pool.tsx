// A new scaling pool starts with no servers and grows from a rule. Creating
// it saves the pool and the Cloud's policy, turned on and with the new rule,
// in one request, so neither exists without the other.
import { useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  CapacityPolicy,
  Connection,
  Pool,
  Provider,
  ScalingPoolInputBody,
  Target,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import {
  CloudError,
  Field,
  SetupReads,
} from "@/components/cloud-targets/infrastructure-ui"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import {
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { staleText } from "@/lib/capacity"
import {
  blankRule,
  growProblem,
  scalingPoolRequest,
  tighterFrom,
  draftOf,
  type RuleDraft,
} from "@/lib/capacity-policy"
import { gpuTitle, nvidiaKinds } from "@/lib/gpu"

import { RuleFields } from "./capacity-rule"

const unsavedText =
  "Save or discard your changes to the growth policy first. A new pool is created with the policy as it's saved."
// Named as the growth policy's switch is labelled, so you can find it.
const turnsOnText = "Creating it also turns on Let pools on this Cloud grow."
const offText =
  "Automatic expansion isn't on for this Loomplane yet, so this pool stays empty until you add a server to it. You can still create it."

export function NewScalingPool({
  cloudId,
  policy,
  automatic,
  unsaved,
  pools,
  accounts,
  reload,
  onClose,
  onCreated,
}: {
  cloudId: string
  /** The policy as it's saved. */
  policy: CapacityPolicy
  automatic: boolean
  /** The growth policy has changes nobody saved yet. */
  unsaved: boolean
  pools: Pool[]
  accounts: Connection[]
  reload: () => Promise<unknown>
  onClose: () => void
  onCreated: () => void
}) {
  const providers = useQuery(api.useProvidersList)
  const targets = useQuery(api.useOrganizationCloudTargetsList, {
    path: { cloudId },
  })
  const create = useMutation(api.useCloudCapacityCreateScalingPool)
  return (
    <Dialog open onOpenChange={(o) => !o && !create.isPending && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>New scaling pool</DialogTitle>
          <DialogDescription>
            It starts with no servers. When a deployment needs room, Loomplane
            adds servers to it from this rule, within the Cloud&apos;s limits.
          </DialogDescription>
        </DialogHeader>
        <SetupReads
          reads={[
            { label: "Providers", query: providers },
            { label: "Targets", query: targets },
          ]}
        >
          <ScalingPoolForm
            cloudId={cloudId}
            policy={policy}
            automatic={automatic}
            unsaved={unsaved}
            pools={pools}
            accounts={accounts}
            providers={providers.data?.providers ?? []}
            targets={(targets.data?.targets ?? []).filter(
              (t): t is Target => !!t
            )}
            busy={create.isPending}
            submit={(body) => create.mutateAsync({ path: { cloudId }, body })}
            reload={reload}
            onClose={onClose}
            onCreated={onCreated}
          />
        </SetupReads>
      </DialogContent>
    </Dialog>
  )
}

function ScalingPoolForm({
  cloudId,
  policy,
  automatic,
  unsaved,
  pools,
  accounts,
  providers,
  targets,
  busy,
  submit,
  reload,
  onClose,
  onCreated,
}: {
  cloudId: string
  policy: CapacityPolicy
  automatic: boolean
  unsaved: boolean
  pools: Pool[]
  accounts: Connection[]
  providers: Provider[]
  targets: Target[]
  busy: boolean
  submit: (body: ScalingPoolInputBody) => Promise<unknown>
  reload: () => Promise<unknown>
  onClose: () => void
  onCreated: () => void
}) {
  const [name, setName] = useState("")
  const [kind, setKind] = useState("")
  const [rule, setRule] = useState<RuleDraft>(blankRule)
  const [stale, setStale] = useState(false)
  const [error, setError] = useState("")
  const trimmed = name.trim()
  // Built from the saved policy as it is now, so a refetch after a 409
  // moves both the revision and what is sent.
  const request = scalingPoolRequest({
    policy,
    pools,
    targets,
    providers,
    name: trimmed,
    kind,
    rule,
  })
  const blocked = unsaved ? unsavedText : growProblem(policy, kind)
  const problems = blocked
    ? [blocked]
    : !trimmed
      ? ["Name the pool."]
      : request.problems
  const valid = !blocked && !!trimmed && !!request.policy && !!request.template
  const cloud = draftOf(policy)

  // A different kind offers different images and sizes, so those start over.
  const chooseKind = (next: string) => {
    setKind(next)
    setRule((r) => ({
      ...r,
      image: "",
      allowedSizes: [],
      limits: r.limits
        ? tighterFrom(cloud.ceilings, cloud.gpus[next] ?? "")
        : null,
    }))
  }

  async function onCreate() {
    if (!valid) return
    setError("")
    setStale(false)
    try {
      await submit({
        name: trimmed,
        ...(kind ? { acceleratorKind: kind } : {}),
        expectedRevision: policy.revision,
        policy: request.policy!,
        template: request.template!,
      })
      void reload().catch(() => undefined)
      onCreated()
    } catch (err) {
      if (statusOf(err) === 409) {
        setStale(true)
        void reload().catch(() => undefined)
      } else {
        setError(messageOf(err, "The pool wasn't created."))
      }
    }
  }

  return (
    <form
      aria-label="Scaling pool"
      className="grid min-w-0 gap-4"
      onSubmit={(e) => {
        e.preventDefault()
        void onCreate()
      }}
    >
      <p className="text-[12.5px] leading-relaxed text-muted-foreground">
        {[
          automatic ? "" : offText,
          policy.enabled
            ? automatic
              ? "Pools on this Cloud may grow already."
              : ""
            : turnsOnText,
        ]
          .filter(Boolean)
          .join(" ")}
      </p>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Field label="Pool name">
          <Input
            aria-label="Pool name"
            autoComplete="off"
            maxLength={64}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field label="GPU kind">
          <Select
            value={kind}
            onValueChange={(e) => chooseKind(String(e ?? ""))}
          >
            <SelectTrigger aria-label="GPU kind" className="w-full min-w-0">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">None, a CPU pool</SelectItem>
              {nvidiaKinds.map((k) => (
                <SelectItem key={k} value={k}>
                  {gpuTitle(k)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      </div>
      <RuleFields
        cloudId={cloudId}
        kind={kind}
        value={rule}
        accounts={accounts}
        set={(patch) => setRule((r) => ({ ...r, ...patch }))}
        onTighter={(on) =>
          setRule((r) => ({
            ...r,
            limits: on
              ? tighterFrom(cloud.ceilings, cloud.gpus[kind] ?? "")
              : null,
          }))
        }
      />
      <div className="grid gap-3" aria-live="polite">
        {problems.length > 0 && (
          <ul className="grid gap-1 text-xs text-muted-foreground">
            {problems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        )}
        <CloudError message={stale ? staleText : error} />
      </div>
      <DialogFooter className="flex-wrap">
        <Button
          type="button"
          variant="outline"
          disabled={busy}
          onClick={onClose}
        >
          Cancel
        </Button>
        <Button type="submit" disabled={busy || !valid}>
          {busy && <Spinner />}
          Create pool
        </Button>
      </DialogFooter>
    </form>
  )
}
