// Connecting an integration on a page of its own, with a switcher to move
// to any other. What you run yourself (an SSH server with Docker, an image
// registry, a Kubernetes cluster) gets presets that fill what's usual for
// where it runs, and a side panel with the commands that fetch the rest.
// Every other cloud gets its connect form, with its help beside it.
import { CheckIcon, ChevronsUpDownIcon, UploadIcon } from "lucide-react"
import { type FormEvent, type ReactNode, useId, useRef, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useConnectionCloudsList,
  useConnectionInClusterCreate,
  useConnectionsCreate,
  useConnectionsList,
} from "@/client/forge/loomplane/hooks"
import type { CloudForm, Connection } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import { ConnectPanel } from "@/components/cloud-connections"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { CopyButton } from "@/components/runs"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
  FieldTitle,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { cloudLabel } from "@/lib/clouds"
import {
  type Preset,
  type RegistryAuth,
  type Secret,
  type SelfHosted,
  connectCredential,
  connectSettings,
  currentContext,
  isSelfHosted,
  kubeconfigContexts,
  kubeconfigProblems,
  presets,
} from "@/lib/connect-presets"
import { cloudPurposes, serverKinds } from "@/lib/integrations"
import { useOrganization } from "@/lib/organization"
import { useNavigate } from "@/lib/router"
import { cn } from "@/lib/utils"

const titles: Record<SelfHosted, string> = {
  server: "SSH server",
  registry: "Image registry",
  kubernetes: "Kubernetes",
}

const intros: Record<SelfHosted, string> = {
  server:
    "Loomplane runs your services on a machine's Docker, over SSH. Pick where it is, and the form fills what it can.",
  registry:
    "Loomplane pushes the images it builds here, and your servers pull them from it.",
  kubernetes:
    "Loomplane deploys into one namespace of your cluster. Start from where the cluster runs, then adjust.",
}

type Values = Record<string, string>

export function ConnectCloudPage({ cloud }: { cloud: string }) {
  const { can } = useOrganization()
  const forms = useQuery(useConnectionCloudsList)
  const entry = forms.data?.clouds.find((c) => c.cloud === cloud)
  const title =
    entry?.title || (isSelfHosted(cloud) ? titles[cloud] : cloudLabel(cloud))
  const unknown = !!forms.data && !entry && !isSelfHosted(cloud)
  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Integrations", to: "/integrations" },
          { label: `Connect ${title}` },
        ]}
      />
      <Page className="gap-6">
        <PageHeading
          eyebrow={<CloudGlyph cloud={cloud} size="md" className="mb-2" />}
          title={`Connect ${title}`}
          description={
            isSelfHosted(cloud)
              ? intros[cloud]
              : cloudPurposes[cloud]
                ? `Loomplane deploys to ${cloudPurposes[cloud]}. It checks the credential with ${title} before keeping it.`
                : `Loomplane checks the credential with ${title} before keeping it.`
          }
          actions={<Switcher cloud={cloud} />}
        />
        {!can("cloud_account:manage") ? (
          <Empty preview={false}>
            <EmptyHeader>
              <EmptyTitle>Only admins connect infrastructure</EmptyTitle>
              <EmptyDescription>
                Ask an admin of this workspace to connect it, or to give you the
                permission.
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : forms.status === "error" ? (
          <QueryError
            error={forms.error}
            fallback="The list of integrations couldn't be loaded."
            retry={forms.refetch}
          />
        ) : unknown ? (
          <Empty preview={false}>
            <EmptyHeader>
              <EmptyTitle>Loomplane can't connect to {cloud}</EmptyTitle>
              <EmptyDescription>
                It isn't one of the integrations this Loomplane offers. Its
                plugin may have been removed.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Switcher cloud={cloud} />
            </EmptyContent>
          </Empty>
        ) : isSelfHosted(cloud) ? (
          <ConnectForm key={cloud} cloud={cloud} />
        ) : !entry ? (
          <Skeleton className="h-80 w-full rounded-[14px]" />
        ) : (
          <ManagedCloudForm key={cloud} entry={entry} />
        )}
      </Page>
    </>
  )
}

/**
 * Switcher moves to any other integration without going back to the
 * catalog, grouped the way the catalog groups them.
 */
function Switcher({ cloud }: { cloud: string }) {
  const navigate = useNavigate()
  const forms = useQuery(useConnectionCloudsList)
  const connections = useQuery(useConnectionsList)
  const list = forms.data?.clouds ?? []
  const current = list.find((c) => c.cloud === cloud)
  const count = (c: string) =>
    (connections.data?.connections ?? []).filter((x) => x.cloud === c).length
  const groups = [
    {
      label: "Clouds",
      items: list.filter((f) => !serverKinds.has(f.cloud)),
    },
    {
      label: "Servers",
      items: list.filter((f) => serverKinds.has(f.cloud)),
    },
  ].filter((g) => g.items.length > 0)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button variant="outline" aria-label="Connect something else" />
        }
      >
        <CloudGlyph cloud={cloud} size="xs" />
        {current?.title ??
          (isSelfHosted(cloud) ? titles[cloud] : cloudLabel(cloud))}
        <ChevronsUpDownIcon className="text-muted-foreground" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-64">
        {groups.map((g, i) => (
          <DropdownMenuGroup key={g.label}>
            {i > 0 && <DropdownMenuSeparator />}
            <DropdownMenuLabel>{g.label}</DropdownMenuLabel>
            {g.items.map((f) => {
              const n = count(f.cloud)
              return (
                <DropdownMenuItem
                  key={f.cloud}
                  onClick={() =>
                    void navigate({
                      to: "/integrations/connect/$cloud",
                      params: { cloud: f.cloud },
                    })
                  }
                >
                  <CloudGlyph cloud={f.cloud} size="xs" />
                  <span className="min-w-0 flex-1 truncate">{f.title}</span>
                  {n > 0 && (
                    <span className="font-mono text-[11px] text-muted-foreground">
                      {n} connected
                    </span>
                  )}
                  {f.cloud === cloud && <CheckIcon />}
                </DropdownMenuItem>
              )
            })}
          </DropdownMenuGroup>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/**
 * ManagedCloudForm is a managed cloud's connect form, the same one the dialog
 * shows, with what the provider says about its credential beside it.
 */
function ManagedCloudForm({ entry }: { entry: CloudForm }) {
  const navigate = useNavigate()
  return (
    <div className="grid items-start gap-5 lg:grid-cols-[minmax(0,1fr)_320px]">
      <div className="min-w-0">
        <ConnectPanel
          cloud={entry.cloud}
          onCancel={() => void navigate({ to: "/integrations" })}
          onConnected={(c) =>
            void navigate({ to: "/clouds/accounts/$id", params: { id: c.id } })
          }
        />
      </div>
      <aside className="grid gap-3 lg:sticky lg:top-16">
        <section className="overflow-hidden rounded-[14px] bg-card ring-1 ring-border">
          <div className="border-b px-4 py-3">
            <h2 className="text-sm font-semibold">About {entry.title}</h2>
            {cloudPurposes[entry.cloud] && (
              <p className="text-[12.5px] text-muted-foreground">
                {cloudPurposes[entry.cloud]}
              </p>
            )}
          </div>
          <div className="grid gap-2.5 px-4 py-3.5 text-[12.5px] leading-relaxed text-muted-foreground">
            {entry.help && <p>{entry.help}</p>}
            <p>
              {entry.methods.includes("oauth")
                ? "Signing in is quickest: you approve Loomplane on " +
                  entry.title +
                  " and come back here."
                : entry.methods.includes("keyless")
                  ? "Keyless keeps no secret: " +
                    entry.title +
                    " trusts Loomplane's identity instead."
                  : "Loomplane checks each permission without changing anything, and says which are missing."}
            </p>
          </div>
        </section>
      </aside>
    </div>
  )
}

function ConnectForm({ cloud }: { cloud: SelfHosted }) {
  const navigate = useNavigate()
  const invalidate = useInvalidate()
  const create = useMutation(useConnectionsCreate)
  const createInCluster = useMutation(useConnectionInClusterCreate)
  const forms = useQuery(useConnectionCloudsList)
  const entry = forms.data?.clouds.find((c) => c.cloud === cloud)
  // Loomplane's own cluster connects as its service account, so it comes
  // first where the operator allows it.
  const inClusterOffered =
    cloud === "kubernetes" && !!entry?.methods.includes("in_cluster")
  const [kubeconfigChosen, setKubeconfigChosen] = useState(false)
  const inCluster = inClusterOffered && !kubeconfigChosen

  const [preset, setPreset] = useState<Preset | null>(null)
  const [name, setName] = useState("")
  const [values, setValues] = useState<Values>({})
  const [secret, setSecret] = useState<Secret>({ auth: "anonymous" })
  const [registry, setRegistry] = useState(false)
  const [error, setError] = useState("")
  const busy = create.isPending || createInCluster.isPending

  const set = (key: string) => (v: string) =>
    setValues((cur) => ({ ...cur, [key]: v }))
  const setS = (key: keyof Secret) => (v: string) =>
    setSecret((cur) => ({ ...cur, [key]: v }))

  const apply = (p: Preset) => {
    setPreset(p)
    // A preset starts the settings over; what was pasted as a credential
    // stays, since no preset can know it.
    setValues({ ...p.settings })
    setSecret((cur) => ({ ...cur, ...p.secret }))
    setRegistry(!!p.registry)
    if (p.name) setName(p.name)
    if (cloud === "kubernetes") setKubeconfigChosen(true)
    setError("")
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    const settings = connectSettings(cloud, values, registry)
    try {
      const c: Connection = inCluster
        ? await createInCluster.mutateAsync({
            body: { name: name.trim(), settings },
          })
        : await create.mutateAsync({
            body: {
              cloud,
              name: name.trim(),
              credential: connectCredential(cloud, secret),
              settings,
            },
          })
      invalidate(useConnectionsList)
      toast.success(
        c.status === "ready"
          ? `${c.name} is connected and ready`
          : `${c.name} is connected, but needs attention`
      )
      void navigate({ to: "/clouds/accounts/$id", params: { id: c.id } })
    } catch (err) {
      setError(messageOf(err, `The ${titles[cloud]} wasn't connected.`))
    }
  }

  const fields = {
    values,
    set,
    secret,
    setS,
    setSecret,
    name,
    setName,
  }

  return (
    <form
      onSubmit={(e) => void submit(e)}
      className="grid items-start gap-5 lg:grid-cols-[minmax(0,1fr)_320px]"
    >
      <div className="grid min-w-0 gap-4">
        <Presets cloud={cloud} chosen={preset} onChoose={apply} />
        {cloud === "server" && <ServerFields {...fields} />}
        {cloud === "registry" && <RegistryFields {...fields} />}
        {cloud === "kubernetes" && (
          <KubernetesFields
            {...fields}
            inClusterOffered={inClusterOffered}
            inCluster={inCluster}
            onInCluster={(on) => setKubeconfigChosen(!on)}
            registry={registry}
            onRegistry={setRegistry}
          />
        )}
        {error && (
          <Alert variant="destructive">
            <AlertTitle>Not connected</AlertTitle>
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <div className="flex flex-wrap items-center justify-end gap-2 border-t pt-4">
          <span className="mr-auto text-xs text-muted-foreground">
            Loomplane checks it before keeping anything. The credential is
            stored encrypted and never shown again.
          </span>
          <Button
            type="button"
            variant="ghost"
            onClick={() => void navigate({ to: "/integrations" })}
          >
            Cancel
          </Button>
          <Button type="submit" disabled={busy}>
            {busy && <Spinner />}
            {cloud === "kubernetes" && registry && values.registry_hostname
              ? "Check, set up and connect"
              : "Check and connect"}
          </Button>
        </div>
      </div>
      <Guide cloud={cloud} preset={preset} inCluster={inCluster} />
    </form>
  )
}

/* ------------------------------------------------------------------------ */
/* Presets                                                                    */

function Presets({
  cloud,
  chosen,
  onChoose,
}: {
  cloud: SelfHosted
  chosen: Preset | null
  onChoose: (p: Preset) => void
}) {
  return (
    <section aria-labelledby="presets" className="grid gap-2.5">
      <div className="flex items-baseline gap-2">
        <h2 id="presets" className="text-sm font-semibold">
          Start from
        </h2>
        <span className="text-[12.5px] text-muted-foreground">
          Fills the usual values. You can change any of them.
        </span>
      </div>
      <div
        role="radiogroup"
        aria-labelledby="presets"
        className="grid gap-2.5 sm:grid-cols-2 xl:grid-cols-3"
      >
        {presets[cloud].map((p) => {
          const on = chosen?.id === p.id
          return (
            <button
              key={p.id}
              type="button"
              role="radio"
              aria-checked={on}
              onClick={() => onChoose(p)}
              className={cn(
                "grid gap-1 rounded-xl bg-card p-3.5 text-left ring-1 ring-border transition-shadow outline-none hover:ring-foreground/25 focus-visible:ring-3 focus-visible:ring-ring/40",
                on && "ring-2 ring-primary hover:ring-primary"
              )}
            >
              <span className="flex items-center gap-2 text-[13.5px] font-semibold">
                {p.label}
                {on && <CheckIcon className="ml-auto size-4 text-primary" />}
              </span>
              <span className="text-[12.5px] leading-relaxed text-muted-foreground">
                {p.description}
              </span>
            </button>
          )
        })}
      </div>
    </section>
  )
}

/** Guide is the side panel: how to get each value the preset can't fill. */
function Guide({
  cloud,
  preset,
  inCluster,
}: {
  cloud: SelfHosted
  preset: Preset | null
  inCluster: boolean
}) {
  const steps = inCluster ? [] : (preset ?? presets[cloud][0]).steps
  return (
    <aside className="grid gap-3 lg:sticky lg:top-16">
      <section className="overflow-hidden rounded-[14px] bg-card ring-1 ring-border">
        <div className="border-b px-4 py-3">
          <h2 className="text-sm font-semibold">Get the values</h2>
          <p className="text-[12.5px] text-muted-foreground">
            {inCluster
              ? "Nothing to copy: Loomplane uses its own service account."
              : preset
                ? `For ${preset.label.toLowerCase()}.`
                : `For ${presets[cloud][0].label.toLowerCase()}. Pick a preset to change it.`}
          </p>
        </div>
        {steps.length > 0 && (
          <ol className="grid gap-3.5 px-4 py-3.5">
            {steps.map((s, i) => (
              <li
                key={s.title}
                className="grid grid-cols-[1.25rem_minmax(0,1fr)] gap-2"
              >
                <span className="flex size-5 items-center justify-center rounded-full border font-mono text-[11px] text-muted-foreground">
                  {i + 1}
                </span>
                <div className="grid min-w-0 gap-1.5">
                  <span className="text-[13px] font-medium">{s.title}</span>
                  {s.command && (
                    <div className="flex items-start gap-1 rounded-lg bg-subtle py-1.5 pr-1 pl-2.5 ring-1 ring-border">
                      <code className="min-w-0 flex-1 font-mono text-[11.5px] leading-relaxed break-all">
                        {s.command}
                      </code>
                      <CopyButton value={s.command} label="Copy command" />
                    </div>
                  )}
                  {s.note && (
                    <span className="text-xs leading-relaxed text-muted-foreground">
                      {s.note}
                    </span>
                  )}
                </div>
              </li>
            ))}
          </ol>
        )}
      </section>
    </aside>
  )
}

/* ------------------------------------------------------------------------ */
/* Building blocks                                                            */

type FieldProps = {
  values: Values
  set: (key: string) => (v: string) => void
  secret: Secret
  setS: (key: keyof Secret) => (v: string) => void
  setSecret: React.Dispatch<React.SetStateAction<Secret>>
  name: string
  setName: (v: string) => void
}

function Section({
  title,
  description,
  aside,
  children,
}: {
  title: string
  description?: ReactNode
  aside?: ReactNode
  children?: ReactNode
}) {
  return (
    <section
      aria-label={title}
      className="overflow-hidden rounded-[14px] bg-card ring-1 ring-border"
    >
      <div className="flex items-center gap-3 border-b px-4 py-3">
        <div className="grid min-w-0 flex-1 gap-0.5">
          <h2 className="text-sm font-semibold">{title}</h2>
          {description && (
            <p className="text-[12.5px] leading-relaxed text-muted-foreground">
              {description}
            </p>
          )}
        </div>
        {aside}
      </div>
      {children && <FieldGroup className="gap-4 p-4">{children}</FieldGroup>}
    </section>
  )
}

/** More folds away what most connections leave as it is. */
function More({ title, children }: { title: string; children: ReactNode }) {
  return (
    <details className="group rounded-[14px] bg-card ring-1 ring-border">
      <summary className="cursor-pointer px-4 py-3 text-[13px] font-semibold select-none">
        {title}
        <span className="ml-2 text-[12.5px] font-normal text-muted-foreground">
          Optional
        </span>
      </summary>
      <FieldGroup className="gap-4 border-t p-4">{children}</FieldGroup>
    </details>
  )
}

function Text({
  id,
  label,
  hint,
  value,
  onChange,
  placeholder,
  mono,
  required,
  type,
}: {
  id: string
  label: string
  hint?: ReactNode
  value: string | undefined
  onChange: (v: string) => void
  placeholder?: string
  mono?: boolean
  required?: boolean
  type?: "password"
}) {
  return (
    <Field>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <Input
        id={id}
        type={type}
        value={value ?? ""}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        required={required}
        autoComplete="off"
        spellCheck={false}
        className={cn(mono && "font-mono text-[13px]")}
      />
      {hint && <FieldDescription>{hint}</FieldDescription>}
    </Field>
  )
}

/** Paste is a credential you paste or load from a file. */
function Paste({
  id,
  label,
  hint,
  value,
  onChange,
  placeholder,
  required,
  accept,
  children,
}: {
  id: string
  label: string
  hint?: ReactNode
  value: string | undefined
  onChange: (v: string) => void
  placeholder?: string
  required?: boolean
  accept?: string
  children?: ReactNode
}) {
  const file = useRef<HTMLInputElement>(null)
  return (
    <Field>
      <div className="flex items-center justify-between gap-2">
        <FieldLabel htmlFor={id}>{label}</FieldLabel>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => file.current?.click()}
        >
          <UploadIcon />
          Choose file
        </Button>
        <input
          ref={file}
          type="file"
          accept={accept}
          className="hidden"
          aria-hidden="true"
          tabIndex={-1}
          onChange={(e) => {
            const f = e.target.files?.[0]
            if (f) void f.text().then(onChange)
            e.target.value = ""
          }}
        />
      </div>
      <Textarea
        id={id}
        value={value ?? ""}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        spellCheck={false}
        autoComplete="off"
        required={required}
        className="max-h-64 min-h-28 font-mono text-xs md:text-xs"
      />
      {children}
      {hint && <FieldDescription>{hint}</FieldDescription>}
    </Field>
  )
}

type Option = { value: string; label: string; hint?: string }

/** Choice is a segmented control for a setting with a few known values. */
function Choice({
  label,
  hint,
  value,
  options,
  onChange,
}: {
  label: string
  hint?: ReactNode
  value: string | undefined
  options: Option[]
  onChange: (v: string) => void
}) {
  const id = useId()
  const chosen = options.find((o) => o.value === (value ?? ""))
  return (
    <Field>
      <FieldTitle id={id}>{label}</FieldTitle>
      <div
        role="radiogroup"
        aria-labelledby={id}
        className="flex w-fit max-w-full flex-wrap gap-0.5 rounded-[10px] bg-muted p-0.75"
      >
        {options.map((o) => {
          const on = o === chosen
          return (
            <button
              key={o.value}
              type="button"
              role="radio"
              aria-checked={on}
              onClick={() => onChange(o.value)}
              className={cn(
                "h-7.5 rounded-lg px-3 text-[12.5px] font-medium outline-none focus-visible:ring-3 focus-visible:ring-ring/40",
                on
                  ? "bg-background text-foreground shadow-xs"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              {o.label}
            </button>
          )
        })}
      </div>
      {(chosen?.hint ?? hint) && (
        <FieldDescription>{chosen?.hint ?? hint}</FieldDescription>
      )}
    </Field>
  )
}

function NameField({
  name,
  setName,
  placeholder,
}: FieldProps & { placeholder: string }) {
  return (
    <Text
      id="connect-name"
      label="Name"
      value={name}
      onChange={setName}
      placeholder={placeholder}
      hint="How it shows up in Loomplane."
      required
    />
  )
}

/* ------------------------------------------------------------------------ */
/* SSH server                                                                 */

function ServerFields(props: FieldProps) {
  const { values, set, secret, setS } = props
  return (
    <>
      <Section
        title="Server"
        description="Where Docker runs, and who Loomplane signs in as."
      >
        <NameField {...props} placeholder="Build box" />
        <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_7rem]">
          <Text
            id="connect-host"
            label="Host"
            value={values.host}
            onChange={set("host")}
            placeholder="203.0.113.10 or box.example.com"
            mono
            required
          />
          <Text
            id="connect-port"
            label="Port"
            value={values.port}
            onChange={set("port")}
            placeholder="22"
            mono
          />
        </div>
        <Text
          id="connect-user"
          label="SSH user"
          value={values.user}
          onChange={set("user")}
          placeholder="deploy"
          hint="It needs access to the Docker socket, usually through the docker group."
          mono
          required
        />
      </Section>
      <Section
        title="Keys"
        description="Loomplane signs in with the private key, and only trusts the server whose host key matches."
      >
        <Paste
          id="connect-private-key"
          label="Private key"
          value={secret.privateKey}
          onChange={setS("privateKey")}
          placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
          required
        />
        <Text
          id="connect-passphrase"
          label="Passphrase (optional)"
          type="password"
          value={secret.passphrase}
          onChange={setS("passphrase")}
          hint="Only for a key that's encrypted."
        />
        <Text
          id="connect-host-key"
          label="Server's host key"
          value={values.host_key}
          onChange={set("host_key")}
          placeholder="ssh-ed25519 AAAAC3Nza..."
          hint="Paste ssh-keyscan's line as it is. The host in front is dropped."
          mono
          required
        />
      </Section>
      <More title="Docker and builds">
        <Text
          id="connect-socket"
          label="Docker socket"
          value={values.docker_socket}
          onChange={set("docker_socket")}
          placeholder="/var/run/docker.sock"
          mono
        />
        <Text
          id="connect-advertise"
          label="Published address"
          value={values.advertise_host}
          onChange={set("advertise_host")}
          placeholder="The host above"
          hint="Where services' ports are reached, when it isn't the SSH host."
          mono
        />
        <Choice
          label="Source builds"
          value={values.build_workers}
          onChange={set("build_workers")}
          options={[
            {
              value: "",
              label: "Not here",
              hint: "Loomplane only runs images built elsewhere on it.",
            },
            {
              value: "dedicated",
              label: "Build here",
              hint: "Choose it for a machine that runs nothing but your own code: builds run your repositories' Dockerfiles on it.",
            },
          ]}
        />
      </More>
    </>
  )
}

/* ------------------------------------------------------------------------ */
/* Image registry                                                             */

function RegistryFields(props: FieldProps) {
  const { values, set, secret, setS, setSecret } = props
  const auth = secret.auth ?? "anonymous"
  return (
    <>
      <Section title="Registry" description="Where images go.">
        <NameField {...props} placeholder="Team registry" />
        <Text
          id="connect-endpoint"
          label="Endpoint"
          value={values.endpoint}
          onChange={set("endpoint")}
          placeholder="https://registry.example.com"
          hint="HTTPS, except for localhost."
          mono
          required
        />
        <Text
          id="connect-repository"
          label="Repository prefix"
          value={values.repository}
          onChange={set("repository")}
          placeholder="team/apps"
          hint={
            <>
              Lowercase. Images are pushed as{" "}
              <code className="font-mono text-xs">
                {(values.repository || "prefix").trim()}/service:tag
              </code>
              .
            </>
          }
          mono
          required
        />
      </Section>
      <Section title="Sign-in">
        <Choice
          label="How Loomplane signs in"
          value={auth}
          onChange={(v) =>
            setSecret((cur) => ({ ...cur, auth: v as RegistryAuth }))
          }
          options={[
            {
              value: "anonymous",
              label: "Anonymous",
              hint: "For a registry anyone on the network can push to, like a local one.",
            },
            { value: "password", label: "Username and password" },
            { value: "token", label: "Token" },
          ]}
        />
        {auth === "password" && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Text
              id="connect-username"
              label="Username"
              value={secret.username}
              onChange={setS("username")}
              required
            />
            <Text
              id="connect-password"
              label="Password or access token"
              type="password"
              value={secret.password}
              onChange={setS("password")}
              required
            />
          </div>
        )}
        {auth === "token" && (
          <Text
            id="connect-token"
            label="Bearer token"
            type="password"
            value={secret.token}
            onChange={setS("token")}
            required
          />
        )}
      </Section>
      <More title="Token service and certificate">
        <Text
          id="connect-auth-endpoint"
          label="Token service endpoint"
          value={values.auth_endpoint}
          onChange={set("auth_endpoint")}
          placeholder="https://auth.example.com/token"
          hint="Only when the registry hands out tokens from another host, like Docker Hub's auth.docker.io."
          mono
        />
        <Paste
          id="connect-ca"
          label="CA certificate"
          value={secret.ca}
          onChange={setS("ca")}
          placeholder="-----BEGIN CERTIFICATE-----"
          accept=".pem,.crt"
          hint="For a registry whose certificate a private CA signed."
        />
      </More>
    </>
  )
}

/* ------------------------------------------------------------------------ */
/* Kubernetes                                                                 */

function KubernetesFields(
  props: FieldProps & {
    inClusterOffered: boolean
    inCluster: boolean
    onInCluster: (on: boolean) => void
    registry: boolean
    onRegistry: (on: boolean) => void
  }
) {
  const { values, set, secret, setS, inCluster, registry } = props
  const yaml = secret.kubeconfig ?? ""
  const contexts = kubeconfigContexts(yaml)
  const current = currentContext(yaml)
  const problems = yaml.trim() ? kubeconfigProblems(yaml) : []
  return (
    <>
      <Section title="Cluster" description="How Loomplane reaches the API.">
        <NameField {...props} placeholder="Production cluster" />
        {props.inClusterOffered && (
          <Choice
            label="Connect"
            value={inCluster ? "in_cluster" : "kubeconfig"}
            onChange={(v) => props.onInCluster(v === "in_cluster")}
            options={[
              {
                value: "in_cluster",
                label: "The cluster Loomplane runs in",
                hint: "It connects as its own service account. Choose a namespace your operator gave it, never the one Loomplane runs in.",
              },
              { value: "kubeconfig", label: "With a kubeconfig" },
            ]}
          />
        )}
        {!inCluster && (
          <Paste
            id="connect-kubeconfig"
            label="Kubeconfig"
            value={yaml}
            onChange={setS("kubeconfig")}
            placeholder={
              "apiVersion: v1\nclusters:\n- cluster:\n    certificate-authority-data: ..."
            }
            accept=".yaml,.yml,.conf,text/yaml"
            hint="Self-contained: certificates embedded, and a token or client certificate. No CLI sign-in plugins."
            required
          >
            {problems.length > 0 && (
              <Alert>
                <AlertTitle>The API will refuse this kubeconfig</AlertTitle>
                <AlertDescription>
                  <ul className="list-disc pl-4">
                    {problems.map((p) => (
                      <li key={p}>{p}</li>
                    ))}
                  </ul>
                </AlertDescription>
              </Alert>
            )}
          </Paste>
        )}
        <div className="grid gap-4 sm:grid-cols-2">
          {!inCluster &&
            (contexts.length > 1 ? (
              <Choice
                label="Context"
                value={values.context ?? ""}
                onChange={set("context")}
                options={contexts.map((c) => ({
                  value: c === current ? "" : c,
                  label: c,
                }))}
              />
            ) : (
              <Text
                id="connect-context"
                label="Context"
                value={values.context}
                onChange={set("context")}
                placeholder={current || "The kubeconfig's current one"}
                mono
              />
            ))}
          <Text
            id="connect-namespace"
            label="Namespace"
            value={values.namespace}
            onChange={set("namespace")}
            placeholder={inCluster ? "apps" : "The context's, or default"}
            hint="Loomplane deploys only into it."
            mono
            required={inCluster}
          />
        </div>
      </Section>
      <Section
        title="Builds"
        description="Whether Loomplane may build your source in this cluster."
      >
        <Choice
          label="Build workers"
          value={values.build_workers ?? ""}
          onChange={set("build_workers")}
          options={[
            {
              value: "",
              label: "Not here",
              hint: "Loomplane only deploys images built elsewhere.",
            },
            {
              value: "dedicated",
              label: "Dedicated",
              hint: "The cluster runs nothing but your own code, so builds run without a sandbox.",
            },
            {
              value: "shared",
              label: "Shared, sandboxed",
              hint: "Other workloads share the nodes, so each build runs in a Kata sandbox.",
            },
          ]}
        />
        {values.build_workers === "shared" && (
          <Text
            id="connect-runtime-class"
            label="Kata RuntimeClass"
            value={values.build_runtime_class}
            onChange={set("build_runtime_class")}
            placeholder="kata"
            hint="One whose handler is kata or kata-*. Your node admin installs Kata."
            mono
            required
          />
        )}
      </Section>
      <Section
        title="Managed registry"
        description="Loomplane runs a registry in the cluster to build there, installing ingress and cert-manager if they're missing. It needs cluster-admin."
        aside={
          <Switch
            aria-label="Run a registry in this cluster"
            checked={registry}
            onCheckedChange={props.onRegistry}
          />
        }
      >
        {registry && <RegistryOptions {...props} />}
      </Section>
      <More title="Placement overrides">
        <div className="grid gap-4 sm:grid-cols-2">
          <Text
            id="connect-storage-class"
            label="Storage class"
            value={values.storage_class}
            onChange={set("storage_class")}
            placeholder="The cluster's default"
            mono
          />
          <Text
            id="connect-ingress-class"
            label="Ingress class"
            value={values.ingress_class}
            onChange={set("ingress_class")}
            placeholder="The cluster's default"
            mono
          />
          <Text
            id="connect-ingress-address"
            label="Ingress address"
            value={values.ingress_address}
            onChange={set("ingress_address")}
            placeholder="Found from the ingress"
            hint="An IP or hostname, when the one found isn't reachable."
            mono
          />
          <Text
            id="connect-pull-secret"
            label="Image pull Secret"
            value={values.image_pull_secret}
            onChange={set("image_pull_secret")}
            placeholder="None"
            hint="An existing Secret in the namespace, for a private registry."
            mono
          />
        </div>
        <Choice
          label="Architecture"
          value={values.architecture ?? ""}
          onChange={set("architecture")}
          options={[
            { value: "", label: "From the nodes" },
            { value: "amd64", label: "amd64" },
            { value: "arm64", label: "arm64" },
          ]}
        />
      </More>
    </>
  )
}

function RegistryOptions({ values, set }: FieldProps) {
  const network = values.registry_network ?? ""
  return (
    <>
      <Text
        id="connect-registry-hostname"
        label="Registry hostname"
        value={values.registry_hostname}
        onChange={set("registry_hostname")}
        placeholder="registry.example.com"
        hint="A DNS name the cluster's nodes reach. With a verified zone under Domains, Loomplane writes the record."
        mono
        required
      />
      <Choice
        label="Storage"
        value={values.registry_storage ?? ""}
        onChange={set("registry_storage")}
        options={[
          {
            value: "",
            label: "The cluster's",
            hint: "Uses the storage class the cluster already has.",
          },
          {
            value: "local-path",
            label: "Local disk",
            hint: "Installs local-path storage on the node's disk. Good for one node.",
          },
          {
            value: "longhorn",
            label: "Longhorn",
            hint: "Replicated storage. Needs three Linux nodes with Longhorn's prerequisites, and Kubernetes 1.34 or later.",
          },
        ]}
      />
      <Choice
        label="Network"
        value={network}
        onChange={set("registry_network")}
        options={[
          {
            value: "",
            label: "Load balancer",
            hint: "Reuses the cluster's load balancer, or creates one. A cloud may bill for it.",
          },
          {
            value: "node",
            label: "A node's address",
            hint: "Ingress listens on one node. For bare clusters with no load balancer.",
          },
          {
            value: "metallb",
            label: "MetalLB",
            hint: "Installs MetalLB with an address pool you give it.",
          },
        ]}
      />
      {network === "node" && (
        <Text
          id="connect-ingress-node"
          label="Ingress node"
          value={values.ingress_node}
          onChange={set("ingress_node")}
          placeholder="Only needed with several nodes"
          mono
        />
      )}
      {network === "metallb" && (
        <Text
          id="connect-metallb"
          label="MetalLB address pool"
          value={values.load_balancer_addresses}
          onChange={set("load_balancer_addresses")}
          placeholder="192.168.1.240-192.168.1.250"
          hint="Unused CIDRs or IP ranges on the node network, comma separated."
          mono
          required
        />
      )}
      <div className="grid gap-4 sm:grid-cols-3">
        <Choice
          label="Setup"
          value={values.registry_setup ?? ""}
          onChange={set("registry_setup")}
          options={[
            { value: "", label: "Automatic" },
            { value: "existing", label: "I've done it" },
          ]}
        />
        <Choice
          label="DNS"
          value={values.registry_dns ?? ""}
          onChange={set("registry_dns")}
          options={[
            { value: "", label: "Automatic" },
            { value: "manual", label: "Manual" },
          ]}
        />
        <Choice
          label="Maintenance"
          value={values.registry_maintenance ?? ""}
          onChange={set("registry_maintenance")}
          hint="Briefly pauses the registry."
          options={[
            { value: "", label: "Automatic" },
            { value: "manual", label: "Manual" },
          ]}
        />
      </div>
    </>
  )
}
