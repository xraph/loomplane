import { GitBranchIcon, ShieldCheckIcon } from "lucide-react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
import {
  Item,
  ItemContent,
  ItemDescription,
  ItemMedia,
  ItemTitle,
} from "@/components/ui/item"
import { Kbd, KbdGroup } from "@/components/ui/kbd"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { cn } from "@/lib/utils"
import { CopyButton, ProviderGlyph, segmented } from "@/mock/components/bits"
import { type Env, services } from "@/mock/data"
import { ProjectIcon } from "@/lib/project-style"
import { navigate } from "@/mock/router"
import { type DeployRequest, useStore } from "@/mock/store"

export function DeployDialog({
  preset,
  onOpenChange,
}: {
  preset: Partial<DeployRequest> | null
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={preset !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        {/* Keyed so each opening starts from its preset. */}
        {preset !== null && (
          <DeployForm
            key={JSON.stringify(preset)}
            preset={preset}
            done={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function DeployForm({
  preset,
  done,
}: {
  preset: Partial<DeployRequest>
  done: () => void
}) {
  const { deploy, projects } = useStore()
  const findProject = (id: string) => projects.find((p) => p.id === id)
  const [projectId, setProjectId] = useState(preset.project ?? "atlas")
  const proj = findProject(projectId) ?? projects[0]
  const envs = proj?.envs.map((e) => e.env) ?? ["production"]
  const [env, setEnv] = useState<Env>(
    preset.env && envs.includes(preset.env) ? preset.env : (envs[0] as Env)
  )
  const [ref, setRef] = useState(preset.ref ?? proj?.branch ?? "main")
  const svc = services.filter((s) => s.project === projectId)
  const clouds = [...new Set(svc.map((s) => s.provider))]
  const cli = `loomplane deploy ${projectId} --env ${env} --ref ${ref || "main"}`

  const submit = (e?: FormEvent) => {
    e?.preventDefault()
    const id = deploy({ project: projectId, env, ref })
    done()
    navigate(`/deployments/${id}`)
    toast.success(`Deploying ${projectId} to ${env}`, {
      description:
        svc.length === 0
          ? "First deploy. Loomplane reads the repository to find its services."
          : env === "production"
            ? "It will stop for approval after planning."
            : `Building ${svc.length} services from ${ref || "main"}.`,
    })
  }

  return (
    <form
      onSubmit={submit}
      onKeyDown={(e) => {
        if ((e.metaKey || e.ctrlKey) && e.key === "Enter") submit()
      }}
      className="grid gap-5"
    >
      <DialogHeader>
        <DialogTitle>New deployment</DialogTitle>
        <DialogDescription>
          Loomplane plans the change first, so you see what it will touch before
          anything runs.
        </DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <Field>
          <FieldLabel>Project</FieldLabel>
          <Select
            items={projects.map((p) => ({ label: p.name, value: p.id }))}
            value={projectId}
            onValueChange={(v) => {
              const next = findProject(String(v))
              if (!next) return
              setProjectId(next.id)
              const allowed = next.envs.map((x) => x.env)
              if (!allowed.includes(env)) setEnv(allowed[0] as Env)
              setRef(next.branch)
            }}
          >
            <SelectTrigger className="w-full" aria-label="Project">
              <SelectValue>
                {(v: string) => {
                  const p = findProject(v)
                  if (!p) return v
                  return (
                    <span className="flex items-center gap-2">
                      <ProjectIcon icon={p.icon} color={p.color} size="xs" />
                      {p.name}
                    </span>
                  )
                }}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              {projects.map((p) => (
                <SelectItem key={p.id} value={p.id}>
                  <ProjectIcon icon={p.icon} color={p.color} size="xs" />
                  {p.name}
                  <span className="text-muted-foreground">{p.description}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field>
          <FieldLabel>Environment</FieldLabel>
          <ToggleGroup
            variant="outline"
            spacing={0}
            value={[env]}
            onValueChange={(v) => {
              const next = v[0] as Env | undefined
              if (next) setEnv(next)
            }}
            className="w-full"
          >
            {envs.map((e) => (
              <ToggleGroupItem
                key={e}
                value={e}
                className={cn(segmented, "flex-1 capitalize")}
              >
                {e}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        </Field>
        <Field>
          <FieldLabel htmlFor="deploy-ref">Branch or commit</FieldLabel>
          <InputGroup>
            <InputGroupAddon>
              <GitBranchIcon />
            </InputGroupAddon>
            <InputGroupInput
              id="deploy-ref"
              value={ref}
              onChange={(e) => setRef(e.target.value)}
              className="font-mono text-sm"
              autoComplete="off"
              spellCheck={false}
            />
          </InputGroup>
        </Field>
      </FieldGroup>

      <Item variant="muted" className="flex-col items-stretch gap-3">
        <div className="flex items-start gap-2.5">
          <ItemMedia className="gap-1">
            {clouds.map((c) => (
              <ProviderGlyph key={c} id={c} />
            ))}
          </ItemMedia>
          <ItemContent>
            <ItemTitle>
              Rebuilds {svc.length} {svc.length === 1 ? "service" : "services"}{" "}
              across {clouds.length} {clouds.length === 1 ? "cloud" : "clouds"}
            </ItemTitle>
            {env === "production" && (
              <ItemDescription className="flex items-start gap-1.5">
                <ShieldCheckIcon className="mt-0.5 size-3.5 shrink-0" />
                Production deploys stop after planning until an admin approves.
              </ItemDescription>
            )}
          </ItemContent>
        </div>
        <InputGroup className="bg-background">
          <InputGroupInput
            readOnly
            value={cli}
            aria-label="CLI command"
            className="font-mono text-xs"
          />
          <InputGroupAddon align="inline-end">
            <CopyButton value={cli} label="Copy CLI command" />
          </InputGroupAddon>
        </InputGroup>
      </Item>

      <DialogFooter>
        <Button type="button" variant="outline" onClick={done}>
          Cancel
        </Button>
        <Button type="submit">
          Deploy
          <KbdGroup className="ml-1 hidden sm:inline-flex">
            <Kbd className="bg-primary-foreground/15 text-primary-foreground">
              ⌘
            </Kbd>
            <Kbd className="bg-primary-foreground/15 text-primary-foreground">
              ↵
            </Kbd>
          </KbdGroup>
        </Button>
      </DialogFooter>
    </form>
  )
}
