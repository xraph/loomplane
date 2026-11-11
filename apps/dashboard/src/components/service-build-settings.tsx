import { useInvalidate } from "@forge-go/client-react"
import { useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useManifestsCurrent,
  useManifestsEdit,
} from "@/client/forge/loomplane/hooks"
import type {
  Binding,
  Environment,
  Project,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useOrganization } from "@/lib/organization"

type Commands = { install?: string; build?: string }
type Vercel = {
  services: boolean
  root?: string
  service_commands?: Record<string, Commands>
}
type Settings = { provider?: string; commands?: Commands; vercel?: Vercel }
type Entry = Settings & { environments?: Record<string, Settings> }

export function ServiceBuildSettings({
  project,
  name,
  environments,
  deployed,
}: {
  project: Project
  name: string
  environments: Environment[]
  deployed: Record<string, Binding | undefined>
}) {
  const current = useQuery(useManifestsCurrent, { path: { id: project.id } })
  const [environment, setEnvironment] = useState(environments[0]?.name ?? "")
  const services = current.data?.manifest?.services as
    Record<string, Entry> | undefined
  const entry = services?.[name]
  const override = entry?.environments?.[environment]
  const settings = { ...entry, ...override }
  const provider =
    settings.provider ??
    current.data?.manifest?.defaults?.provider ??
    deployed[environment]?.provider
  if (current.data && (!entry || provider !== "vercel")) return null
  return (
    <section
      aria-label="Vercel build settings"
      className="grid gap-3 rounded-xl p-4 ring-1 ring-border"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">Vercel build settings</h2>
        <label className="flex items-center gap-2 text-xs">
          Environment
          <select
            aria-label="Build environment"
            className="h-8 rounded-md border bg-background px-2"
            value={environment}
            onChange={(e) => setEnvironment(e.target.value)}
          >
            {environments.map((e) => (
              <option key={e.id} value={e.name}>
                {e.name}
              </option>
            ))}
          </select>
        </label>
      </div>
      {current.status === "error" ? (
        <QueryError
          error={current.error}
          retry={current.refetch}
          fallback="Build settings could not be loaded."
        />
      ) : !current.data ? (
        <Skeleton className="h-16" />
      ) : (
        <BuildForm
          key={`${environment}:${JSON.stringify(settings)}`}
          project={project}
          name={name}
          environment={environment}
          settings={settings}
        />
      )}
    </section>
  )
}

function BuildForm({
  project,
  name,
  environment,
  settings,
}: {
  project: Project
  name: string
  environment: string
  settings: Settings
}) {
  const { can } = useOrganization()
  const save = useMutation(useManifestsEdit)
  const invalidate = useInvalidate()
  const [grouped, setGrouped] = useState(settings.vercel?.services ?? false)
  const [root, setRoot] = useState(settings.vercel?.root ?? ".")
  const [member, setMember] = useState("")
  const [commands, setCommands] = useState<Commands>(settings.commands ?? {})
  const [members, setMembers] = useState<Record<string, Commands>>(
    settings.vercel?.service_commands ?? {}
  )
  const active = grouped ? (members[member] ?? {}) : commands
  const disabled = !can("project:update") || save.isPending
  const change = (key: keyof Commands, value: string | undefined) => {
    const next = { ...active, [key]: value }
    if (grouped) setMembers({ ...members, [member]: next })
    else setCommands(next)
  }
  const submit = async () => {
    try {
      await save.mutateAsync({
        path: { id: project.id },
        body: {
          placement: {
            section: "services",
            name,
            environment,
            commands: grouped ? {} : commands,
            vercel: {
              services: grouped,
              ...(grouped ? { root, serviceCommands: members } : {}),
            },
          },
        },
      })
      invalidate(useManifestsCurrent)
      toast.success("Build settings saved for the next deployment.")
    } catch (error) {
      toast.error(messageOf(error, "Build settings were not saved."))
    }
  }
  return (
    <form
      className="grid gap-3"
      onSubmit={(e) => {
        e.preventDefault()
        void submit()
      }}
    >
      <label className="flex items-center gap-2 text-sm">
        <Checkbox
          checked={grouped}
          disabled={disabled}
          onCheckedChange={(value) => setGrouped(value === true)}
        />
        Deploy as one Vercel Services project
      </label>
      {grouped && (
        <>
          <p className="text-xs text-muted-foreground">
            The services in vercel.json deploy and roll back together. Keep one
            Loomplane manifest entry for this project. Routes and private
            connections stay in vercel.json.
          </p>
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="grid gap-1 text-xs">
              Project root
              <Input
                value={root}
                disabled={disabled}
                onChange={(e) => setRoot(e.target.value)}
                placeholder="."
              />
            </label>
            <label className="grid gap-1 text-xs">
              Vercel service name
              <Input
                value={member}
                disabled={disabled}
                onChange={(e) => setMember(e.target.value)}
                placeholder="Name from vercel.json"
                list="vercel-command-members"
              />
            </label>
            <datalist id="vercel-command-members">
              {Object.keys(members).map((key) => (
                <option key={key} value={key} />
              ))}
            </datalist>
          </div>
        </>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        {(["install", "build"] as const).map((key) => (
          <CommandField
            key={`${grouped}:${member}:${key}`}
            label={key === "install" ? "Install command" : "Build command"}
            value={active[key]}
            disabled={disabled || (grouped && !member.trim())}
            onChange={(value) => change(key, value)}
          />
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit" disabled={disabled || (grouped && !root.trim())}>
          Save build settings
        </Button>
        <p className="text-xs text-muted-foreground">
          Applies to the next deployment of {environment}. Keep secrets in
          Variables.
        </p>
      </div>
    </form>
  )
}

function CommandField({
  label,
  value,
  disabled,
  onChange,
}: {
  label: string
  value?: string
  disabled: boolean
  onChange: (value: string | undefined) => void
}) {
  const [custom, setCustom] = useState(value !== undefined && value !== "")
  const mode = custom ? "custom" : value === "" ? "skip" : "default"
  return (
    <div className="grid content-start gap-1">
      <label className="flex items-center justify-between gap-2 text-xs">
        {label}
        <select
          aria-label={`${label} mode`}
          className="h-8 rounded-md border bg-background px-2"
          value={mode}
          disabled={disabled}
          onChange={(e) => {
            setCustom(e.target.value === "custom")
            onChange(e.target.value === "default" ? undefined : "")
          }}
        >
          <option value="default">Repository default</option>
          <option value="custom">Custom</option>
          <option value="skip">Skip</option>
        </select>
      </label>
      {custom && (
        <Input
          aria-label={label}
          value={value ?? ""}
          maxLength={4096}
          disabled={disabled}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
    </div>
  )
}
