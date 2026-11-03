// The connect form for what you run yourself: an SSH server with Docker,
// an image registry, or a Kubernetes cluster. Presets fill what's usual for
// where it runs, a guide has the commands that fetch the rest, and whatever
// the form's choices make irrelevant stays out of sight. It fills a page of
// its own, or a dialog where a connection is wanted without leaving.
import { CheckIcon, UploadIcon } from "lucide-react"
import {
  type FormEvent,
  type ReactNode,
  useEffect,
  useId,
  useRef,
  useState,
} from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useConnectionCloudsList,
  useConnectionInClusterCreate,
  useConnectionsCreate,
  useConnectionsList,
  useConnectionsReplace,
} from "@/client/forge/loomplane/hooks"
import type { Connection, Setting } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { CopyButton } from "@/components/runs"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { DialogFooter } from "@/components/ui/dialog"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
  FieldTitle,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import {
  type Preset,
  type RegistryAuth,
  type Step,
  type Secret,
  type SelfHosted,
  connectCredential,
  connectSettings,
  currentContext,
  kubeconfigContexts,
  kubeconfigProblems,
  knownSettings,
  presets,
} from "@/lib/connect-presets"
import { cn } from "@/lib/utils"

export const selfHostedTitles: Record<SelfHosted, string> = {
  server: "SSH server",
  registry: "Image registry",
  kubernetes: "Kubernetes",
}

type Values = Record<string, string>

/**
 * SelfHostedConnect connects cloud, or with replacing set, gives that
 * connection a new credential, keeping its settings unless they're changed.
 */
export function SelfHostedConnect({
  cloud,
  layout,
  replacing,
  onConnected,
  onCancel,
  onDirtyChange,
}: {
  cloud: SelfHosted
  layout: "page" | "dialog"
  replacing?: Connection
  onConnected: (connection: Connection) => void
  onCancel: () => void
  /** Whether anything has been typed, so leaving can be confirmed. */
  onDirtyChange?: (dirty: boolean) => void
}) {
  const invalidate = useInvalidate()
  const create = useMutation(useConnectionsCreate)
  const createInCluster = useMutation(useConnectionInClusterCreate)
  const replace = useMutation(useConnectionsReplace)
  const forms = useQuery(useConnectionCloudsList)
  const entry = forms.data?.clouds.find((c) => c.cloud === cloud)
  // Loomplane's own cluster connects as its service account, so it comes
  // first where the operator allows it. A replaced connection keeps the
  // kind of credential it has.
  const inClusterOffered =
    !replacing &&
    cloud === "kubernetes" &&
    !!entry?.methods.includes("in_cluster")
  const [kubeconfigChosen, setKubeconfigChosen] = useState(false)
  const inCluster = inClusterOffered && !kubeconfigChosen

  // A replaced connection starts from the settings it has.
  const [initial] = useState<Values>(() =>
    Object.fromEntries(
      Object.entries(replacing?.details ?? {}).map(([k, v]) => [k, String(v)])
    )
  )
  const [preset, setPreset] = useState<Preset | null>(null)
  const [name, setName] = useState("")
  const [values, setValues] = useState<Values>(initial)
  const [secret, setSecret] = useState<Secret>({ auth: "anonymous" })
  const [registry, setRegistry] = useState(!!initial.registry_hostname)
  const [error, setError] = useState("")
  const busy =
    create.isPending || createInCluster.isPending || replace.isPending

  const dirty =
    !!name.trim() ||
    Object.entries(values).some(([k, v]) => v !== (initial[k] ?? "")) ||
    Object.entries(secret).some(([k, v]) => k !== "auth" && !!v?.trim())
  useEffect(() => onDirtyChange?.(dirty), [dirty, onDirtyChange])

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
    // A form in a dialog can sit inside another form's dialog.
    e.preventDefault()
    e.stopPropagation()
    setError("")
    const settings = connectSettings(cloud, values, registry)
    try {
      const c: Connection = replacing
        ? await replace.mutateAsync({
            path: { id: replacing.id },
            body: { credential: connectCredential(cloud, secret), settings },
          })
        : inCluster
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
      onDirtyChange?.(false)
      onConnected(c)
    } catch (err) {
      setError(
        messageOf(
          err,
          replacing
            ? "The credential wasn't replaced."
            : `The ${selfHostedTitles[cloud]} wasn't connected.`
        )
      )
    }
  }

  const fields: FieldProps = {
    values,
    set,
    secret,
    setS,
    setSecret,
    name,
    setName,
    replacing: !!replacing,
  }
  // Settings the API asks for that this form has no place for yet, such as
  // one added since, still get a field.
  const other = (
    (inCluster ? entry?.inClusterSettings : entry?.settings) ?? []
  ).filter((st) => !knownSettings[cloud].has(st.key))
  const setup = cloud === "kubernetes" && registry && !!values.registry_hostname
  const label = replacing
    ? setup
      ? "Check, set up and replace"
      : "Check and replace"
    : setup
      ? "Check, set up and connect"
      : "Check and connect"
  const page = layout === "page"

  return (
    <form
      onSubmit={(e) => void submit(e)}
      className={cn(
        "grid items-start gap-5",
        page && "lg:grid-cols-[minmax(0,1fr)_320px]"
      )}
    >
      <div className="grid min-w-0 gap-4">
        {!replacing && (
          <Presets cloud={cloud} chosen={preset} onChoose={apply} />
        )}
        {/* Beside the form when there's room; otherwise folded under the
            presets, before the fields it helps fill. */}
        {!replacing && (
          <div className={cn(page && "lg:hidden")}>
            <Guide cloud={cloud} preset={preset} inCluster={inCluster} folded />
          </div>
        )}
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
        {other.length > 0 && (
          <OtherSettings fields={other} values={values} set={set} />
        )}
        {error && (
          <Alert variant="destructive">
            <AlertTitle>
              {replacing ? "Not replaced" : "Not connected"}
            </AlertTitle>
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        {page ? (
          <div className="flex flex-wrap items-center justify-end gap-2 border-t pt-4">
            <span className="mr-auto text-xs text-muted-foreground">
              Loomplane checks it before keeping anything. The credential is
              stored encrypted and never shown again.
            </span>
            <Button type="button" variant="ghost" onClick={onCancel}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy}>
              {busy && <Spinner />}
              {label}
            </Button>
          </div>
        ) : (
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onCancel}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy}>
              {busy && <Spinner />}
              {label}
            </Button>
          </DialogFooter>
        )}
      </div>
      {page && (
        <div className="hidden lg:sticky lg:top-16 lg:block">
          <Guide cloud={cloud} preset={preset} inCluster={inCluster} />
        </div>
      )}
    </form>
  )
}

function OtherSettings({
  fields,
  values,
  set,
}: {
  fields: Setting[]
  values: Values
  set: (key: string) => (v: string) => void
}) {
  const required = fields.some((f) => f.required)
  const body = fields.map((f) => (
    <Text
      key={f.key}
      id={`connect-other-${f.key}`}
      label={f.label}
      value={values[f.key]}
      onChange={set(f.key)}
      required={f.required}
      mono
    />
  ))
  // A required one can't hide in a fold.
  return required ? (
    <Section title="Other settings">{body}</Section>
  ) : (
    <More title="Other settings">{body}</More>
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
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
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

/**
 * Guide is how to get each value the preset can't fill: a side panel on a
 * page, folded away in a dialog, where there's no room beside the form.
 */
function Guide({
  cloud,
  preset,
  inCluster,
  folded = false,
}: {
  cloud: SelfHosted
  preset: Preset | null
  inCluster: boolean
  folded?: boolean
}) {
  const steps = inCluster ? [] : (preset ?? presets[cloud][0]).steps
  const note = inCluster
    ? "Nothing to copy: Loomplane uses its own service account."
    : preset
      ? `For ${preset.label.toLowerCase()}.`
      : `For ${presets[cloud][0].label.toLowerCase()}. Pick a preset to change it.`
  if (folded)
    return steps.length > 0 ? (
      <details className="rounded-[14px] bg-card ring-1 ring-border">
        <summary className="cursor-pointer px-4 py-3 text-[13px] font-semibold select-none">
          Get the values
          <span className="ml-2 text-[12.5px] font-normal text-muted-foreground">
            {note}
          </span>
        </summary>
        <div className="border-t">
          <Steps steps={steps} />
        </div>
      </details>
    ) : null
  return (
    <aside className="grid gap-3">
      <section className="overflow-hidden rounded-[14px] bg-card ring-1 ring-border">
        <div className="border-b px-4 py-3">
          <h2 className="text-sm font-semibold">Get the values</h2>
          <p className="text-[12.5px] text-muted-foreground">{note}</p>
        </div>
        {steps.length > 0 && <Steps steps={steps} />}
      </section>
    </aside>
  )
}

function Steps({ steps }: { steps: Step[] }) {
  return (
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
                <code className="min-w-0 flex-1 font-mono text-[11.5px] leading-relaxed wrap-anywhere">
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
  /** A replaced connection keeps its name. */
  replacing: boolean
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
      <div>
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
  replacing,
}: FieldProps & { placeholder: string }) {
  if (replacing) return null
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
