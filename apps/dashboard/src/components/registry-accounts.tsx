import { type FormEvent, useId, useState } from "react"
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
import { Input } from "@/components/ui/input"
import { connectCredential } from "@/lib/connect-presets"
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
      setError(
        messageOf(
          e,
          "Couldn’t connect this registry.",
          `Loomplane could not verify registry access to registry.digitalocean.com/${name}`
        )
      )
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

export function GitHubRegistrySetup({
  busy,
  error,
  onSubmit,
  onCancel,
  onConnected,
}: Connected & {
  busy: boolean
  error: string
  onSubmit: (connection: {
    name: string
    credential: string
    settings: Record<string, string>
  }) => Promise<void>
  onCancel: () => void
}) {
  const github = useQuery(useGithubStatus)
  const registries = useQuery(useConnectionsList)
  const listId = useId()
  const [method, setMethod] = useState("")
  const [owner, setOwner] = useState<string>()
  const [username, setUsername] = useState("")
  const [token, setToken] = useState("")
  const owners = [
    ...new Set(
      (github.data?.installations ?? []).map((i) => i.account.toLowerCase())
    ),
  ]
  const namespace = owner ?? (owners.length === 1 ? owners[0] : "")
  const repository = namespace.trim().toLowerCase()
  const validOwner = /^[a-z0-9][a-z0-9_-]{0,38}$/.test(repository)
  const hintId = useId()
  const saved = (registries.data?.connections ?? []).filter(
    (c) =>
      c.cloud === "registry" &&
      c.status === "ready" &&
      c.details?.endpoint === "https://ghcr.io"
  )
  const selected =
    method === "new"
      ? undefined
      : (saved.find((c) => c.id === method) ?? saved[0])
  if (registries.status === "error")
    return (
      <QueryError
        error={registries.error}
        retry={registries.refetch}
        fallback="Couldn’t load registry connections."
      />
    )
  if (!registries.data) return <p>Loading registry connections…</p>

  const ready = selected || (validOwner && username.trim() && token.trim())
  function submit(e: FormEvent) {
    e.preventDefault()
    e.stopPropagation()
    if (busy || !ready) return
    if (selected) {
      onConnected(selected)
      return
    }
    void onSubmit({
      name: `GitHub Packages · ${repository}`,
      settings: { endpoint: "https://ghcr.io", repository },
      credential: connectCredential("registry", {
        auth: "password",
        username,
        password: token.trim(),
      }),
    })
  }
  return (
    <form
      aria-label="GitHub Packages sign-in"
      onSubmit={submit}
      className="grid w-full max-w-3xl gap-4 rounded-xl border bg-card p-4 text-sm"
    >
      <h3 className="font-semibold">GitHub Packages sign-in</h3>
      {saved.length > 0 && (
        <AccountSelect
          label="Sign in with"
          value={selected?.id ?? "new"}
          options={[
            ...saved.map((c) => ({
              value: c.id,
              label: `${c.name} · ghcr.io/${c.details?.repository ?? ""}`,
            })),
            { value: "new", label: "New publishing token" },
          ]}
          choose={setMethod}
        />
      )}
      {!selected && (
        <>
          <label className="grid gap-1 font-medium">
            Publish images under
            <Input
              value={namespace}
              list={listId}
              aria-invalid={!!repository && !validOwner}
              aria-describedby={hintId}
              required
              autoCapitalize="none"
              spellCheck={false}
              placeholder="GitHub user or organization"
              onChange={(e) => setOwner(e.target.value)}
            />
          </label>
          <p
            id={hintId}
            className={
              repository && !validOwner
                ? "text-xs text-destructive"
                : "text-xs text-muted-foreground"
            }
          >
            {repository && !validOwner ? (
              "Enter just the GitHub user or organization, without a repository path. Loomplane adds the service name."
            ) : (
              <>
                Images publish to{" "}
                <code>ghcr.io/{repository || "owner"}/&lt;service&gt;</code>.
              </>
            )}
          </p>
          <datalist id={listId}>
            {owners.map((account) => (
              <option key={account} value={account} />
            ))}
          </datalist>
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="grid gap-1 font-medium">
              GitHub username
              <Input
                value={username}
                required
                autoComplete="username"
                autoCapitalize="none"
                spellCheck={false}
                onChange={(e) => setUsername(e.target.value)}
              />
            </label>
            <label className="grid gap-1 font-medium">
              Personal access token (classic)
              <Input
                type="password"
                required
                value={token}
                autoComplete="new-password"
                onChange={(e) => setToken(e.target.value)}
              />
            </label>
          </div>
          <p className="text-xs text-muted-foreground">
            Your builder needs package write access to publish images.{" "}
            <a
              className="underline"
              href="https://github.com/settings/tokens/new?scopes=write:packages"
              target="_blank"
              rel="noreferrer"
            >
              Create a classic token with write:packages
            </a>
            .
          </p>
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
        </>
      )}
      <div className="flex flex-wrap items-center justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={!ready || busy}>
          {selected
            ? "Use registry"
            : busy
              ? "Checking access…"
              : "Check and connect"}
        </Button>
      </div>
    </form>
  )
}
