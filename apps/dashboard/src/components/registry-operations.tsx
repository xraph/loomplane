import { useEffect, useState } from "react"
import { toast } from "sonner"
import { useMutation, useQuery } from "@/client/hooks"
import {
  useRegistriesAction,
  useRegistriesGet,
} from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  RegistryActionInputBody,
} from "@/client/forge/loomplane/types"
import { messageOf } from "@/client/errors"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Checkbox } from "@/components/ui/checkbox"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { QueryError } from "@/components/query-error"
import { NativeEmpty } from "@/components/native-recovery-empty"

export function RegistryOperations({
  connection,
  mayManage,
}: {
  connection: Connection
  mayManage: boolean
}) {
  const [open, setOpen] = useState(false)
  if (
    connection.cloud !== "kubernetes" ||
    !connection.details.registry_hostname
  )
    return null
  return (
    <div className="min-w-0">
      <Button
        size="sm"
        variant="ghost"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        Registry operations
      </Button>
      {open && (
        <RegistryOperationsContent
          connection={connection}
          mayManage={mayManage}
        />
      )}
    </div>
  )
}

function RegistryOperationsContent({
  connection,
  mayManage,
}: {
  connection: Connection
  mayManage: boolean
}) {
  const query = useQuery(useRegistriesGet, {
    path: { connectionId: connection.id },
  })
  const action = useMutation(useRegistriesAction)
  const [confirm, setConfirm] = useState<RegistryActionInputBody | null>(null)
  const [hostname, setHostname] = useState("")
  const [deleteData, setDeleteData] = useState(false)
  const [removeSetup, setRemoveSetup] = useState(false)
  const state = query.data
  const { refetch } = query
  useEffect(() => {
    const timer = window.setInterval(() => {
      if (!document.hidden) void refetch().catch(() => undefined)
    }, 15000)
    return () => window.clearInterval(timer)
  }, [refetch])
  const busy =
    action.isPending || !!state?.action || state?.status === "removed"
  async function run(kind: string, backupId?: string) {
    if (!state) return
    const input = {
      kind,
      backupId,
      generation: connection.generation,
      hostname: String(connection.details.registry_hostname),
    }
    if (["restore", "remove", "cleanup", "backup", "upgrade"].includes(kind)) {
      setConfirm(input)
      setHostname("")
      setDeleteData(false)
      setRemoveSetup(false)
      return
    }
    await submit(input)
  }
  async function submit(body: RegistryActionInputBody) {
    try {
      await action.mutateAsync({ path: { connectionId: connection.id }, body })
      setConfirm(null)
      await query.refetch()
      toast.success("Registry operation queued")
    } catch (error) {
      toast.error(
        messageOf(error, "The registry operation could not be queued.")
      )
    }
  }
  return (
    <section
      aria-label="Registry maintenance"
      className="grid min-w-0 gap-3 rounded-lg border p-3"
    >
      {query.error ? (
        <QueryError
          error={query.error}
          fallback="Registry status could not be loaded."
          retry={query.refetch}
        />
      ) : !state ? (
        <p className="text-xs text-muted-foreground">
          Loading registry status...
        </p>
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
            <span>
              {state.status.replaceAll("_", " ")}
              {state.action ? ` · ${state.action} · ${state.phase}` : ""} · DNS{" "}
              {connection.details.registry_dns === "manual"
                ? "manual"
                : state.dnsReady
                  ? "managed"
                  : "waiting"}
            </span>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => void query.refetch().catch(() => undefined)}
            >
              Refresh
            </Button>
          </div>
          {state.message && (
            <Alert>
              <AlertDescription className="break-words">
                {state.message}
              </AlertDescription>
            </Alert>
          )}
          <p className="text-xs text-muted-foreground">
            {connection.details.registry_maintenance === "manual"
              ? "Automatic maintenance is disabled."
              : "Automatic maintenance checks bundled controller updates daily, rotates credentials every 30 days, backs up daily, and collects unreferenced image blobs weekly."}{" "}
            Backups and cleanup briefly stop the registry. Tagged images and
            rollback manifests are retained.
          </p>
          {mayManage && state.action && (
            <Button
              size="sm"
              variant="outline"
              disabled={action.isPending}
              onClick={() => void run("retry")}
            >
              Review and retry current operation
            </Button>
          )}
          {mayManage && (
            <div className="flex flex-wrap gap-2">
              {[
                ["sync", "Sync DNS"],
                ["backup", "Back up now"],
                ["rotate", "Rotate credentials"],
                ["cleanup", "Clean unused blobs"],
                ["upgrade", "Upgrade controllers"],
                ["remove", "Remove registry"],
              ].map(([kind, label]) => (
                <Button
                  key={kind}
                  size="sm"
                  variant="outline"
                  disabled={busy}
                  onClick={() => void run(kind)}
                >
                  {label}
                </Button>
              ))}
            </div>
          )}
          {state.backups.length === 0 ? (
            <NativeEmpty title="No registry backups yet">
              Your first successful backup will appear here. You can start one
              after the registry is running.
            </NativeEmpty>
          ) : (
            <div className="grid gap-2">
              <p className="text-xs font-medium">Saved backups</p>
              {[...state.backups]
                .reverse()
                .slice(0, 10)
                .map((backup) => (
                  <div
                    key={backup.id}
                    className="flex flex-wrap items-center justify-between gap-2 border-t pt-2 text-xs"
                  >
                    <span>
                      {new Date(backup.createdAt).toLocaleString()} ·{" "}
                      {(backup.size / 1048576).toFixed(1)} MiB
                    </span>
                    {mayManage && (
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={busy}
                        onClick={() => void run("restore", backup.id)}
                      >
                        Restore
                      </Button>
                    )}
                  </div>
                ))}
            </div>
          )}
          {confirm && (
            <div className="grid gap-2 rounded-md border p-3 text-xs">
              <p>
                {confirm.kind === "remove"
                  ? "Remove the registry and its managed DNS record. Detach workloads and targets first. Storage is retained unless you choose to delete it."
                  : confirm.kind === "restore"
                    ? "Restore this backup over the registry's images. Loomplane first saves a backup of the current data. The registry stays offline until restore finishes."
                    : confirm.kind === "upgrade"
                      ? "Apply compatible controller updates bundled with Loomplane. This can restart shared ingress, certificate and storage controllers."
                      : "Pause registry writes while this operation runs. Image pulls may be temporarily unavailable."}
              </p>
              {confirm.kind === "remove" && (
                <Label className="flex items-center gap-2 text-xs">
                  <Checkbox
                    checked={deleteData}
                    onCheckedChange={(value) => {
                      setDeleteData(value === true)
                      if (value !== true) setRemoveSetup(false)
                    }}
                  />
                  Delete image storage after a fresh backup
                </Label>
              )}
              {confirm.kind === "remove" && (
                <Label className="flex items-center gap-2 text-xs">
                  <Checkbox
                    checked={removeSetup}
                    onCheckedChange={(value) => {
                      setRemoveSetup(value === true)
                      if (value === true) setDeleteData(true)
                    }}
                  />
                  Also remove unused managed controllers. Retained volumes must
                  be deleted first.
                </Label>
              )}
              {["remove", "restore"].includes(confirm.kind) && (
                <>
                  <Label htmlFor={`registry-confirm-${connection.id}`}>
                    Type {confirm.hostname} to confirm
                  </Label>
                  <Input
                    id={`registry-confirm-${connection.id}`}
                    value={hostname}
                    onChange={(event) => setHostname(event.target.value)}
                  />
                </>
              )}
              <div className="flex flex-wrap gap-2">
                <Button
                  size="sm"
                  disabled={
                    action.isPending ||
                    (["remove", "restore"].includes(confirm.kind) &&
                      hostname !== confirm.hostname)
                  }
                  onClick={() =>
                    void submit({
                      ...confirm,
                      deleteData: confirm.kind === "remove" && deleteData,
                      removeSetup: confirm.kind === "remove" && removeSetup,
                    })
                  }
                >
                  Confirm {confirm.kind}
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => setConfirm(null)}
                >
                  Cancel
                </Button>
              </div>
            </div>
          )}
        </>
      )}
    </section>
  )
}
