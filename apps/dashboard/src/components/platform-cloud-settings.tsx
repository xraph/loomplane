import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  usePlatformCloudSettingsGet,
  usePlatformCloudSettingsSet,
} from "@/client/forge/loomplane/hooks"
import type { SettingsView } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import {
  NumberField,
  gibOf,
  selectClass,
  toCount,
} from "@/components/platform-cloud-parts"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"

/** PlatformCloudSettings holds attach mode, limits for new workspaces, DNS and the registry. */
export function PlatformCloudSettings() {
  const settings = useQuery(usePlatformCloudSettingsGet)
  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-base font-medium">Settings</h2>
      {settings.status === "error" && (
        <Alert variant="destructive">
          <AlertDescription>
            {messageOf(settings.error, "Settings couldn't be loaded.")}
          </AlertDescription>
        </Alert>
      )}
      {!settings.data && settings.status !== "error" && (
        <Skeleton className="h-40 w-full rounded-xl" />
      )}
      {settings.data && <SettingsForm view={settings.data} />}
    </section>
  )
}

function SettingsForm({ view }: { view: SettingsView }) {
  const save = useMutation(usePlatformCloudSettingsSet)
  const invalidate = useInvalidate()
  const [attach, setAttach] = useState(view.attach || "manual")
  const [limits, setLimits] = useState({
    vcpus: String(view.defaultLimits.vcpus),
    mem: String(gibOf(view.defaultLimits.memoryMib)),
    vms: String(view.defaultLimits.vms),
    builds: String(view.defaultLimits.concurrentBuilds),
  })
  const [apex, setApex] = useState(view.apex)
  const [provider, setProvider] = useState(view.dns.provider)
  const [zone, setZone] = useState(view.dns.zone)
  const [dnsToken, setDnsToken] = useState("")
  const [dnsAccountId, setDnsAccountId] = useState("")
  const [dnsTeamId, setDnsTeamId] = useState("")
  const [endpoint, setEndpoint] = useState(view.registry.endpoint)
  const [authEndpoint, setAuthEndpoint] = useState(view.registry.authEndpoint)
  const [repository, setRepository] = useState(view.registry.repository)
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [error, setError] = useState("")

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    // Credentials are write-only: send one only when it was typed.
    const typed = (v: string) => (v.trim() ? v.trim() : undefined)
    try {
      await save.mutateAsync({
        body: {
          attach,
          apex: apex.trim(),
          defaultLimits: {
            vcpus: toCount(limits.vcpus),
            memoryMib: toCount(limits.mem) * 1024,
            vms: toCount(limits.vms),
            concurrentBuilds: toCount(limits.builds),
          },
          dns: { provider: provider.trim(), zone: zone.trim() },
          registry: {
            endpoint: endpoint.trim(),
            authEndpoint: authEndpoint.trim(),
            repository: repository.trim(),
          },
          ...(typed(dnsToken) ? { dnsToken: typed(dnsToken) } : {}),
          ...(typed(dnsAccountId) ? { dnsAccountId: typed(dnsAccountId) } : {}),
          ...(typed(dnsTeamId) ? { dnsTeamId: typed(dnsTeamId) } : {}),
          ...(typed(username) ? { registryUsername: typed(username) } : {}),
          ...(password ? { registryPassword: password } : {}),
        },
      })
      setDnsToken("")
      setDnsAccountId("")
      setDnsTeamId("")
      setUsername("")
      setPassword("")
      invalidate(usePlatformCloudSettingsGet)
      toast.success("Settings saved")
    } catch (err) {
      setError(messageOf(err, "The settings weren't saved."))
    }
  }

  return (
    <form onSubmit={submit} className="flex max-w-xl flex-col gap-4">
      <Field>
        <FieldLabel>Which workspaces can use it</FieldLabel>
        <select
          aria-label="Attach"
          className={selectClass}
          value={attach}
          onChange={(e) => setAttach(e.target.value)}
        >
          <option value="manual">Only workspaces I enable</option>
          <option value="all_workspaces">Every workspace</option>
        </select>
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <NumberField
          label="Limit vCPUs"
          value={limits.vcpus}
          onChange={(vcpus) => setLimits({ ...limits, vcpus })}
        />
        <NumberField
          label="Limit memory (GiB)"
          value={limits.mem}
          onChange={(mem) => setLimits({ ...limits, mem })}
        />
        <NumberField
          label="Limit VMs"
          value={limits.vms}
          onChange={(vms) => setLimits({ ...limits, vms })}
        />
        <NumberField
          label="Limit builds at once"
          value={limits.builds}
          onChange={(builds) => setLimits({ ...limits, builds })}
        />
      </div>
      <Field>
        <FieldLabel>Apex domain</FieldLabel>
        <Input
          aria-label="Apex domain"
          value={apex}
          onChange={(e) => setApex(e.target.value)}
        />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field>
          <FieldLabel>DNS provider</FieldLabel>
          <Input
            aria-label="DNS provider"
            value={provider}
            onChange={(e) => setProvider(e.target.value)}
          />
        </Field>
        <Field>
          <FieldLabel>DNS zone</FieldLabel>
          <Input
            aria-label="DNS zone"
            value={zone}
            onChange={(e) => setZone(e.target.value)}
          />
        </Field>
        <Field>
          <FieldLabel>DNS token</FieldLabel>
          <Input
            aria-label="DNS token"
            type="password"
            autoComplete="off"
            value={dnsToken}
            onChange={(e) => setDnsToken(e.target.value)}
          />
        </Field>
        <Field>
          <FieldLabel>DNS account ID</FieldLabel>
          <Input
            aria-label="DNS account ID"
            autoComplete="off"
            value={dnsAccountId}
            onChange={(e) => setDnsAccountId(e.target.value)}
          />
        </Field>
        <Field>
          <FieldLabel>DNS team ID</FieldLabel>
          <Input
            aria-label="DNS team ID"
            autoComplete="off"
            value={dnsTeamId}
            onChange={(e) => setDnsTeamId(e.target.value)}
          />
        </Field>
      </div>
      <FieldDescription>
        {view.dnsCredentialSaved
          ? "A DNS credential is saved. Leave these blank to keep it."
          : "No DNS credential is saved."}
      </FieldDescription>
      <div className="grid grid-cols-2 gap-3">
        <Field>
          <FieldLabel>Registry endpoint</FieldLabel>
          <Input
            aria-label="Registry endpoint"
            value={endpoint}
            onChange={(e) => setEndpoint(e.target.value)}
          />
        </Field>
        <Field>
          <FieldLabel>Registry auth endpoint</FieldLabel>
          <Input
            aria-label="Registry auth endpoint"
            value={authEndpoint}
            onChange={(e) => setAuthEndpoint(e.target.value)}
          />
        </Field>
        <Field>
          <FieldLabel>Registry repository</FieldLabel>
          <Input
            aria-label="Registry repository"
            value={repository}
            onChange={(e) => setRepository(e.target.value)}
          />
        </Field>
        <Field>
          <FieldLabel>Registry username</FieldLabel>
          <Input
            aria-label="Registry username"
            autoComplete="off"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
        </Field>
        <Field>
          <FieldLabel>Registry password</FieldLabel>
          <Input
            aria-label="Registry password"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
      </div>
      <FieldDescription>
        {view.registryCredentialSaved
          ? "A registry credential is saved. Leave these blank to keep it."
          : "No registry credential is saved."}
      </FieldDescription>
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <div>
        <Button type="submit" disabled={save.isPending}>
          Save settings
        </Button>
      </div>
    </form>
  )
}
