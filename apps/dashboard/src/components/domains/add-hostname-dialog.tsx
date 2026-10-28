// Pointing a hostname at one service in one environment: preview the route,
// show the DNS record it writes, then apply it. Not in the mock, which has
// no way to add a hostname from a service.
import { useId, useState } from "react"
import { toast } from "sonner"

import { useDomainsProject } from "@/client/forge/loomplane/hooks"
import type { RouteOperation } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import {
  HostnameField,
  type HostnameValue,
} from "@/components/domains/hostname-field"
import { useRouteLifecycle } from "@/components/domains/use-route-lifecycle"
import { Button } from "@/components/ui/button"
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
import { Link } from "@/lib/router"

export function AddHostnameDialog({
  projectId,
  environment,
  service,
  ports,
  deployed = true,
  open,
  onOpenChange,
}: {
  projectId: string
  environment: { id: string; name: string }
  service: string
  ports: number[]
  /** The service runs in this environment, so a route has a binding. */
  deployed?: boolean
  open: boolean
  onOpenChange(open: boolean): void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="flex flex-col gap-0 overflow-hidden p-0 sm:max-w-[580px]"
      >
        <DialogHeader className="gap-1 px-5 pt-5 pb-1">
          <DialogTitle>Add a domain</DialogTitle>
          {/* not in the mock */}
          <DialogDescription>
            Point a hostname at {service} in {environment.name}.
          </DialogDescription>
        </DialogHeader>
        {open && (
          <Body
            projectId={projectId}
            environment={environment}
            service={service}
            ports={ports}
            deployed={deployed}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function Notice({
  children,
  onClose,
}: {
  children: React.ReactNode
  onClose(): void
}) {
  return (
    <>
      <div className="flex flex-col gap-2 px-5 pt-3 pb-4 text-[13px] text-muted-foreground">
        {children}
      </div>
      <DialogFooter className="m-0 px-5 py-3">
        <Button type="button" variant="outline" onClick={onClose}>
          Close
        </Button>
      </DialogFooter>
    </>
  )
}

function Body({
  projectId,
  environment,
  service,
  ports,
  deployed,
  onClose,
}: {
  projectId: string
  environment: { id: string; name: string }
  service: string
  ports: number[]
  deployed: boolean
  onClose(): void
}) {
  const ids = useId()
  const lifecycle = useRouteLifecycle(projectId)
  const domains = useQuery(useDomainsProject, { path: { projectId } })
  const [value, setValue] = useState<HostnameValue>({
    zoneId: "",
    hostname: "",
  })
  const [port, setPort] = useState(ports[0] ?? 0)
  const [preview, setPreview] = useState<{
    op: RouteOperation
    for: HostnameValue & { port: number }
  } | null>(null)
  const [error, setError] = useState("")

  // not in the mock
  if (ports.length === 0) {
    return (
      <Notice onClose={onClose}>
        <p>
          {service} doesn&apos;t expose a port, so it can&apos;t take a
          hostname.
        </p>
      </Notice>
    )
  }
  // not in the mock
  if (!deployed) {
    return (
      <Notice onClose={onClose}>
        <p>
          {service} isn&apos;t deployed in {environment.name} yet. You can give
          it a hostname once it deploys there.
        </p>
      </Notice>
    )
  }
  const list = domains.data?.domains
  if (list && !list.some((d) => d.verified)) {
    // not in the mock
    return (
      <Notice onClose={onClose}>
        <p>
          {list.length === 0
            ? "No domain is granted to this project yet."
            : "None of the domains granted to this project is verified yet."}
        </p>
        <p>
          <Link
            to="/domains"
            className="font-medium text-primary hover:underline"
          >
            Open Domains
          </Link>
        </p>
      </Notice>
    )
  }

  const current =
    preview &&
    preview.for.zoneId === value.zoneId &&
    preview.for.hostname === value.hostname &&
    preview.for.port === port
      ? preview.op
      : undefined
  const record = current?.desiredRecord
  const external =
    list?.find((d) => d.zoneId === value.zoneId)?.mode === "external"

  const run = async () => {
    setError("")
    try {
      if (!current) {
        const op = await lifecycle.preview({
          zoneId: value.zoneId,
          // The server fills in the Cloud the service's binding runs under.
          cloudId: "",
          environmentId: environment.id,
          hostname: value.hostname,
          port,
          service,
        })
        setPreview({ op, for: { ...value, port } })
        return
      }
      const op = await lifecycle.apply(current)
      if (op.status === "failed") {
        setPreview(null)
        setError(op.message || "The route wasn't applied.")
        return
      }
      // not in the mock
      toast.success(
        `${value.hostname} now points at ${service} in ${environment.name}.`
      )
      onClose()
    } catch {
      // useRouteLifecycle words the refusal. A refused apply starts over
      // from a fresh preview.
      if (current) setPreview(null)
    }
  }

  const shown = error || lifecycle.error
  return (
    <form
      noValidate
      className="flex min-h-0 flex-1 flex-col"
      onSubmit={(e) => {
        e.preventDefault()
        void run()
      }}
    >
      <div className="flex min-h-0 flex-col gap-4 overflow-y-auto px-5 pt-3 pb-4">
        <HostnameField
          projectId={projectId}
          value={value}
          onChange={setValue}
          allowWildcard={false}
        />
        {ports.length === 1 ? (
          <span className="text-[12.5px] text-muted-foreground">
            Port {ports[0]}
          </span>
        ) : (
          <div className="flex flex-col gap-[5px] text-[12.5px] font-medium text-foreground/80">
            <span id={`${ids}-port`}>Port</span>
            <Select
              items={ports.map((p) => ({ label: String(p), value: p }))}
              value={port}
              onValueChange={(v) => setPort(Number(v))}
            >
              <SelectTrigger
                aria-labelledby={`${ids}-port`}
                className="w-40 font-mono"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ports.map((p) => (
                  <SelectItem key={p} value={p}>
                    {p}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}
        {record?.name && (
          <div
            role="group"
            aria-labelledby={`${ids}-record`}
            className="grid gap-1.5 rounded-lg bg-muted px-3 py-2.5"
          >
            <span
              id={`${ids}-record`}
              className="text-[11.5px] font-medium tracking-[0.04em] text-muted-foreground uppercase"
            >
              DNS record
            </span>
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-[12.5px] leading-relaxed">
              <dt className="text-muted-foreground">Type</dt>
              <dd className="font-mono break-all">{record.type}</dd>
              <dt className="text-muted-foreground">Name</dt>
              <dd className="font-mono break-all">{record.name}</dd>
              <dt className="text-muted-foreground">Value</dt>
              <dd className="font-mono break-all">{record.value}</dd>
            </dl>
            {/* not in the mock */}
            <span className="text-xs text-muted-foreground">
              {external
                ? "Add this record at your DNS provider. The route starts serving once it resolves and the certificate is issued."
                : "Loomplane writes this record when you add the domain, then issues a certificate."}
            </span>
          </div>
        )}
        {current?.message && (
          <p className="text-xs text-muted-foreground">{current.message}</p>
        )}
        {(current?.notices ?? []).map((n) => (
          <p key={n} className="text-xs text-muted-foreground">
            {n}
          </p>
        ))}
        {shown && (
          <span role="alert" className="text-xs text-destructive">
            {shown}
          </span>
        )}
      </div>
      <DialogFooter className="m-0 px-5 py-3">
        <Button
          type="button"
          variant="outline"
          disabled={lifecycle.pending}
          onClick={onClose}
        >
          Cancel
        </Button>
        <Button
          type="submit"
          disabled={lifecycle.pending || !value.zoneId || !value.hostname}
        >
          {current ? "Add domain" : "Preview"}
        </Button>
      </DialogFooter>
    </form>
  )
}
