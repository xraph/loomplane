// Asking for a release: pick the staging and production environments and
// which sources move, and Loomplane plans it, collects evidence and has the
// agent review it. The button shows only to someone who may prepare, while
// the server runs the agent and the workspace has it on.
import { ClipboardCheckIcon } from "lucide-react"
import { useState, type FormEvent } from "react"

import { messageOf, statusOf } from "@/client/errors"
import {
  useReleaseReportsList,
  useReleaseReportsPrepare,
  useSourcesList,
} from "@/client/forge/loomplane/hooks"
import type { Environment, Project } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { useAgent, useFeatures } from "@/lib/features"
import { isActive } from "@/lib/release-reports"
import { Link, useNavigate } from "@/lib/router"

/**
 * canPrepare says whether a project can be prepared for release at all: it
 * has an environment of each kind.
 */
export function canPrepare(environments: Environment[] | undefined): boolean {
  return (
    !!environments?.some((e) => e.kind === "staging") &&
    !!environments.some((e) => e.kind === "production")
  )
}

/**
 * PrepareRelease is the "Prepare release" button and its dialog. may is
 * whether the person can create deployments in the project, which the
 * project's page already knows, so this asks nothing more.
 */
export function PrepareRelease({
  project,
  environments,
  may,
}: {
  project: Project
  environments: Environment[] | undefined
  may: boolean
}) {
  const features = useFeatures()
  const agent = useAgent()
  const [open, setOpen] = useState(false)
  if (!features.ai || !agent.enabled || !may || !canPrepare(environments))
    return null
  return (
    <>
      <Button variant="outline" onClick={() => setOpen(true)}>
        <ClipboardCheckIcon />
        Prepare release
      </Button>
      {open && (
        <PrepareDialog
          project={project}
          environments={environments ?? []}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  )
}

type Conflict = { kind: "plan" | "running"; message: string }

function PrepareDialog({
  project,
  environments,
  onClose,
}: {
  project: Project
  environments: Environment[]
  onClose: () => void
}) {
  const staging = environments.filter((e) => e.kind === "staging")
  const production = environments.filter((e) => e.kind === "production")
  const [from, setFrom] = useState(staging[0]?.name ?? "")
  const [to, setTo] = useState(production[0]?.name ?? "")
  const sources = useQuery(useSourcesList, { path: { id: project.id } })
  const aliases = (sources.data?.sources ?? [])
    .filter((s) => !s.removedAt)
    .map((s) => s.alias)
  const [unchecked, setUnchecked] = useState<string[]>([])
  const [replace, setReplace] = useState(false)
  const [conflict, setConflict] = useState<Conflict | null>(null)
  const [error, setError] = useState("")
  const prepare = useMutation(useReleaseReportsPrepare)
  const invalidate = useInvalidate()
  const navigate = useNavigate()
  const scope = aliases.filter((a) => !unchecked.includes(a))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    try {
      const report = await prepare.mutateAsync({
        path: { id: project.id },
        body: {
          from,
          to,
          ...(aliases.length > 0 ? { scope } : {}),
          ...(conflict?.kind === "plan" && replace ? { replace: true } : {}),
        },
      })
      invalidate(useReleaseReportsList)
      onClose()
      void navigate({
        to: "/release-reports/$id",
        params: { id: report.id },
      })
    } catch (err) {
      const message = messageOf(err, "The release couldn't be prepared.")
      // Only an open plan can be replaced, and only a preparation under way
      // has a report to open. Any other 409, such as the agent turned off,
      // is just an error.
      const kind =
        statusOf(err) !== 409
          ? null
          : /already being prepared/i.test(message)
            ? "running"
            : /waiting for a decision/i.test(message)
              ? "plan"
              : null
      if (kind) {
        setReplace(false)
        setConflict({ kind, message })
      } else {
        setConflict(null)
        setError(message)
      }
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={(e) => void submit(e)} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>Prepare a release</DialogTitle>
            <DialogDescription>
              Loomplane plans what staging runs for production, reads the
              evidence and has the agent review it. Nothing ships until the
              seven gates hold or a person approves.
            </DialogDescription>
          </DialogHeader>

          <EnvironmentSelect
            label="From"
            value={from}
            onChange={setFrom}
            environments={staging}
          />
          <EnvironmentSelect
            label="To"
            value={to}
            onChange={setTo}
            environments={production}
          />

          {aliases.length > 0 && (
            <fieldset className="grid gap-2">
              <legend className="mb-1 text-[12.5px] font-medium text-muted-foreground">
                Sources to release
              </legend>
              {aliases.map((a) => (
                <label key={a} className="flex items-center gap-2 text-[13px]">
                  <Checkbox
                    checked={!unchecked.includes(a)}
                    onCheckedChange={(on) =>
                      setUnchecked((u) =>
                        on ? u.filter((x) => x !== a) : [...u, a]
                      )
                    }
                  />
                  {a}
                </label>
              ))}
            </fieldset>
          )}

          <ul className="grid gap-1 text-[12.5px] leading-normal text-muted-foreground">
            <li>
              A release prepared from chat, or one reusing someone else&apos;s
              plan, always waits for a person.
            </li>
            <li>
              A source&apos;s first production release always goes to a
              person.
            </li>
            <li>
              When autonomous promotion is off for this project, the agent still
              asks before production.
            </li>
          </ul>

          {conflict && (
            <Alert>
              <AlertDescription className="grid gap-2">
                <span>{conflict.message}</span>
                {conflict.kind === "plan" ? (
                  <label className="flex items-center gap-2 text-[13px]">
                    <Checkbox
                      checked={replace}
                      onCheckedChange={(on) => setReplace(!!on)}
                    />
                    Replace that plan
                  </label>
                ) : (
                  <RunningLink projectId={project.id} to={to} />
                )}
              </AlertDescription>
            </Alert>
          )}
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={
                prepare.isPending ||
                !from ||
                !to ||
                (aliases.length > 0 && scope.length === 0) ||
                (conflict?.kind === "plan" && !replace) ||
                conflict?.kind === "running"
              }
            >
              {prepare.isPending && <Spinner data-icon="inline-start" />}
              Prepare release
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function EnvironmentSelect({
  label,
  value,
  onChange,
  environments,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  environments: Environment[]
}) {
  const id = `prepare-release-${label.toLowerCase()}`
  const items = environments.map((e) => ({ label: e.name, value: e.name }))
  return (
    <div className="grid gap-1.5">
      <span id={id} className="text-[12.5px] font-medium text-muted-foreground">
        {label}
      </span>
      <Select
        items={items}
        value={value}
        onValueChange={(v) => v && onChange(v)}
      >
        <SelectTrigger className="w-full" aria-labelledby={id}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {items.map((i) => (
            <SelectItem key={i.value} value={i.value}>
              {i.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

// RunningLink points at the report that's already being prepared for the
// environment, so there is nothing to start twice.
function RunningLink({ projectId, to }: { projectId: string; to: string }) {
  const list = useQuery(useReleaseReportsList, { path: { id: projectId } })
  const running = (list.data?.reports ?? []).find(
    (r) => r && isActive(r.state) && r.toEnvironment === to
  )
  if (!running) return null
  return (
    <Link
      to="/release-reports/$id"
      params={{ id: running.id }}
      className="text-primary underline-offset-2 hover:underline"
    >
      Open the release being prepared
    </Link>
  )
}
