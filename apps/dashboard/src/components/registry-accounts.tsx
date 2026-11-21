import { type ReactNode, useId, useState } from "react"
import {
  useConnectionsList,
  useConnectionsCreate,
  useConnectionsDigitalOceanRegistries,
  useGithubStatus,
} from "@/client/forge/loomplane/hooks"
import type { Connection } from "@/client/forge/loomplane/types"
import { messageOf } from "@/client/errors"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Button } from "@/components/ui/button"
import {
  ZeroState,
  ZeroStateHeader,
  ZeroStateDescription,
  ZeroStateTitle,
} from "@/components/zero-state"
import { QueryError } from "@/components/query-error"
import { Link } from "@/lib/router"

type Connected = { onConnected: (connection: Connection) => void }
function AccountSelect({
  label,
  value,
  options,
  choose,
}: {
  label: string
  value: string
  options: { value: string; label: string }[]
  choose: (value: string) => void
}) {
  const id = useId()
  return (
    <div className="grid gap-1">
      <label htmlFor={id} className="text-sm font-medium">
        {label}
      </label>
      <select
        id={id}
        className="h-9 rounded-lg border bg-background px-2 text-sm"
        value={value}
        onChange={(e) => choose(e.target.value)}
      >
        {!options.some((o) => o.value === value) && (
          <option value="">Choose…</option>
        )}
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </div>
  )
}

export function DigitalOceanRegistrySetup({
  onConnected,
  onCancel,
}: Connected & { onCancel: () => void }) {
  const accounts = useQuery(useConnectionsList)
  const [picked, choose] = useState("")
  const available = (accounts.data?.connections ?? []).filter(
    (c) => c.cloud === "digitalocean" && c.status === "ready"
  )
  const account = available.find((c) => c.id === picked) ?? available[0]
  return (
    <section className="grid gap-3 rounded-xl border p-4">
      {accounts.status === "error" ? (
        <QueryError
          error={accounts.error}
          retry={accounts.refetch}
          fallback="Couldn’t load DigitalOcean accounts."
        />
      ) : !accounts.data ? (
        <p>Loading accounts…</p>
      ) : !account ? (
        <ZeroState variant="inline">
          <ZeroStateHeader>
            <ZeroStateTitle>Connect a DigitalOcean account</ZeroStateTitle>
            <ZeroStateDescription>
              Choose an account with registry access in{" "}
              <Link to="/integrations" className="underline">
                Integrations
              </Link>
              .
            </ZeroStateDescription>
          </ZeroStateHeader>
        </ZeroState>
      ) : (
        <>
          <AccountSelect
            label="DigitalOcean account"
            value={account.id}
            options={available.map((c) => ({ value: c.id, label: c.name }))}
            choose={choose}
          />
          <AccountRegistries
            key={account.id}
            account={account}
            connections={accounts.data.connections}
            onConnected={onConnected}
          />
        </>
      )}
      <Button
        type="button"
        variant="ghost"
        className="justify-self-start"
        onClick={onCancel}
      >
        Cancel
      </Button>
    </section>
  )
}

function AccountRegistries({
  account,
  connections,
  onConnected,
}: Connected & { account: Connection; connections: Connection[] }) {
  const query = useQuery(useConnectionsDigitalOceanRegistries, {
    path: { id: account.id },
  })
  const create = useMutation(useConnectionsCreate)
  const invalidate = useInvalidate()
  const [picked, setPicked] = useState("")
  const [error, setError] = useState("")
  const names = query.data?.names ?? []
  const name = names.includes(picked) ? picked : names[0]
  const existing = connections.find(
    (c) =>
      c.cloud === "registry" &&
      c.status === "ready" &&
      c.details?.endpoint === "https://registry.digitalocean.com" &&
      c.details?.repository === name
  )
  async function connect() {
    if (!name) return
    if (existing) {
      onConnected(existing)
      return
    }
    setError("")
    try {
      const result = await create.mutateAsync({
        body: {
          cloud: "registry",
          name: `DigitalOcean · ${name}`,
          credential: "",
          registryAccountId: account.id,
          settings: { repository: name },
        },
      })
      invalidate(useConnectionsList)
      onConnected(result)
    } catch (e) {
      setError(messageOf(e, "Couldn’t connect this registry."))
    }
  }
  if (query.status === "error")
    return (
      <QueryError
        error={query.error}
        retry={query.refetch}
        fallback="Couldn’t list this account’s registries."
      />
    )
  if (!query.data) return <p>Loading registries…</p>
  if (!name)
    return (
      <ZeroState variant="inline">
        <ZeroStateHeader>
          <ZeroStateTitle>No container registries</ZeroStateTitle>
          <ZeroStateDescription>
            Create a registry in this DigitalOcean account, then{" "}
            <button className="underline" onClick={() => void query.refetch()}>
              refresh the list
            </button>
            .
          </ZeroStateDescription>
        </ZeroStateHeader>
      </ZeroState>
    )
  return (
    <>
      <AccountSelect
        label="Registry"
        value={name}
        options={names.map((n) => ({ value: n, label: n }))}
        choose={setPicked}
      />
      <p className="text-xs text-muted-foreground">
        Loomplane gets a short-lived Docker login from {account.name} when it
        needs to build or pull an image.
      </p>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      <Button
        className="justify-self-start"
        disabled={create.isPending}
        onClick={() => void connect()}
      >
        {existing
          ? "Use connected registry"
          : create.isPending
            ? "Checking access…"
            : "Check and connect"}
      </Button>
    </>
  )
}

export function GitHubRegistryAccounts({
  prefix,
  children,
  choose,
  onConnected,
}: Connected & {
  prefix: string
  children: ReactNode
  choose: (owner: string) => void
}) {
  const [another, setAnother] = useState(false)
  const github = useQuery(useGithubStatus)
  const registries = useQuery(useConnectionsList)
  const owners = (github.data?.installations ?? []).map((i) =>
    i.account.toLowerCase()
  )
  const saved = (registries.data?.connections ?? []).filter(
    (c) =>
      c.cloud === "registry" &&
      c.status === "ready" &&
      c.details?.endpoint === "https://ghcr.io"
  )
  if (registries.status === "error")
    return (
      <QueryError
        error={registries.error}
        retry={registries.refetch}
        fallback="Couldn’t load registry connections."
      />
    )
  if (!registries.data) return <p>Loading registry connections…</p>
  if (saved.length > 0 && !another)
    return (
      <section className="grid gap-2 rounded-lg border p-3 text-sm">
        <h3 className="font-medium">Connected GitHub registries</h3>
        {saved.map((c) => (
          <Button
            type="button"
            variant="outline"
            key={c.id}
            className="justify-self-start"
            onClick={() => onConnected(c)}
          >
            Use {c.name} · {String(c.details?.repository ?? "")}
          </Button>
        ))}
        <Button
          type="button"
          variant="ghost"
          className="justify-self-start"
          onClick={() => setAnother(true)}
        >
          Connect another registry
        </Button>
      </section>
    )
  return (
    <>
      {owners.length > 0 && (
        <AccountSelect
          label="Connected GitHub account"
          value={prefix}
          options={[...new Set(owners)].map((owner) => ({
            value: owner,
            label: owner,
          }))}
          choose={choose}
        />
      )}
      <p className="text-sm text-muted-foreground">
        GitHub authorizes repository access and package publishing separately.
        For the first push from your builder, use a classic token with
        write:packages.
      </p>
      {children}
    </>
  )
}
