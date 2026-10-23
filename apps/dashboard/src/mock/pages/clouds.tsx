import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import { Card } from "@/components/ui/card"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select"
import { Checkbox } from "@/components/ui/checkbox"
import { Field as FormField, FieldLabel } from "@/components/ui/field"
import { useEffect, useState } from "react"
import {
  ArrowRightIcon,
  CheckIcon,
  CloudIcon,
  GitCommitHorizontalIcon,
  LoaderCircleIcon,
  PlusIcon,
  RotateCcwIcon,
  TriangleAlertIcon,
} from "lucide-react"

import { CloudGlyph } from "@/components/cloud-glyph"
import { Page } from "@/components/page"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { cn } from "@/lib/utils"
import {
  CloudTabs,
  type CloudTab,
} from "@/components/cloud-targets/infrastructure-ui"
import { MockCloudInfrastructure } from "./cloud-infrastructure"
import { TopBar } from "@/mock/components/top-bar"

type Target = {
  name: string
  provider: string
  cloud: string
  account: string
  accounts: string[]
  services: string[]
  commit: string
}
type Sync = {
  target: string
  from: string
  to: string
  step: number
  failed: boolean
  simulateFailure: boolean
}
const initialTargets: Target[] = [
  {
    name: "frontend",
    provider: "Vercel",
    cloud: "vercel",
    account: "Forge team",
    accounts: ["Forge team", "Xraph production"],
    services: ["dashboard", "website"],
    commit: "8f2c1a9",
  },
  {
    name: "backend",
    provider: "Cloud Run",
    cloud: "gcp",
    account: "forge-production",
    accounts: ["forge-production", "xraph-platform"],
    services: ["api"],
    commit: "3d7b420",
  },
  {
    name: "workers",
    provider: "Kubernetes",
    cloud: "kubernetes",
    account: "production-cluster",
    accounts: ["production-cluster", "compute-cluster"],
    services: ["jobs", "scheduler"],
    commit: "3d7b420",
  },
]
const steps = [
  "Check account",
  "Deploy current release",
  "Check readiness",
  "Switch traffic",
  "Clean up previous deployment",
]

export function CloudsPage() {
  const [tab, setTab] = useState<CloudTab>("Targets")
  const [resetKey, setResetKey] = useState(0)
  const [pools, setPools] = useState<string[]>([])
  const [targets, setTargets] = useState(initialTargets)
  const [selected, setSelected] = useState("frontend")
  const [query, setQuery] = useState("")
  const [switching, setSwitching] = useState(false)
  const [adding, setAdding] = useState(false)
  const [destination, setDestination] = useState("")
  const [newAccount, setNewAccount] = useState("")
  const [newName, setNewName] = useState("")
  const [newProvider, setNewProvider] = useState("Vercel")
  const [simulateFailure, setSimulateFailure] = useState(false)
  const [sync, setSync] = useState<Sync | null>(null)
  const target = targets.find((item) => item.name === selected) ?? targets[0]
  const visible = targets.filter((item) =>
    `${item.name} ${item.provider} ${item.account}`
      .toLowerCase()
      .includes(query.toLowerCase())
  )
  const busy = sync !== null && !sync.failed && sync.step < steps.length
  const targetSync = sync?.target === target.name ? sync : null
  const complete = targetSync !== null && targetSync.step === steps.length
  const chosen = destination === "new" ? newAccount.trim() : destination

  useEffect(() => {
    if (!sync || sync.failed || sync.step >= steps.length) return
    const timer = window.setTimeout(() => {
      if (sync.simulateFailure && sync.step === 2) {
        setSync({ ...sync, failed: true })
        return
      }
      const next = sync.step + 1
      if (next === 4) {
        setTargets((current) =>
          current.map((item) =>
            item.name === sync.target ? { ...item, account: sync.to } : item
          )
        )
      }
      setSync({ ...sync, step: next })
    }, 1100)
    return () => window.clearTimeout(timer)
  }, [sync])

  function openSwitch() {
    setDestination(
      target.accounts.find((account) => account !== target.account) ?? "new"
    )
    setNewAccount("")
    setSwitching(true)
  }

  function startSync() {
    if (!chosen || chosen === target.account) return
    setTargets((current) =>
      current.map((item) =>
        item.name === target.name
          ? {
              ...item,
              account: item.services.length === 0 ? chosen : item.account,
              accounts: [...new Set([...item.accounts, chosen])],
            }
          : item
      )
    )
    setSync({
      target: target.name,
      from: target.account,
      to: chosen,
      step: target.services.length === 0 ? steps.length : 0,
      failed: false,
      simulateFailure,
    })
    setSwitching(false)
  }

  function addTarget() {
    const name = newName.trim()
    if (
      !/^[a-z][a-z0-9-]*$/.test(name) ||
      targets.some((item) => item.name === name)
    )
      return
    const template =
      newProvider === "Server pool"
        ? {
            name: "",
            provider: "Server pool",
            cloud: "server",
            account: pools[0]!,
            accounts: pools,
            services: [],
            commit: "",
          }
        : initialTargets.find((item) => item.provider === newProvider)!
    setTargets([...targets, { ...template, name, services: [], commit: "" }])
    setSelected(name)
    setQuery("")
    setAdding(false)
    setNewName("")
  }

  function reset() {
    setResetKey((k) => k + 1)
    setPools([])
    setTab("Targets")
    setSync(null)
    setTargets(initialTargets)
    setSelected("frontend")
    setQuery("")
    setSimulateFailure(false)
  }

  return (
    <>
      <TopBar
        crumbs={[
          { label: "Forge" },
          { label: "Clouds" },
          { label: "forge-production" },
        ]}
      />
      <Page className="gap-5">
        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
          <span className="flex items-center gap-2">
            <Badge variant="outline">Interactive mock</Badge> Simulated accounts
            and deployments
          </span>
          <div className="flex flex-wrap items-center gap-3">
            <FormField
              orientation="horizontal"
              className="flex min-h-8 items-center gap-2"
            >
              <FieldLabel className="contents">
                <Checkbox
                  checked={simulateFailure}
                  disabled={busy}
                  onCheckedChange={(event) => setSimulateFailure(event)}
                  className="size-4 accent-current"
                />{" "}
                Simulate readiness failure
              </FieldLabel>
            </FormField>
            <Button variant="ghost" size="sm" onClick={reset}>
              <RotateCcwIcon /> Reset
            </Button>
          </div>
        </div>

        <header className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            <span className="flex size-11 shrink-0 items-center justify-center rounded-xl border bg-muted/40">
              <CloudIcon className="size-5" />
            </span>
            <div className="min-w-0">
              <h1 className="text-2xl font-semibold tracking-tight break-words">
                forge-production
              </h1>
              <p className="mt-1 text-sm text-muted-foreground">
                One cloud. Your choice of providers.
              </p>
            </div>
          </div>
          <Badge variant="secondary">Production</Badge>
        </header>

        <div className="flex flex-wrap gap-x-6 gap-y-2 border-y py-3 text-sm">
          <span>
            <strong className="font-medium">{targets.length}</strong>{" "}
            <span className="text-muted-foreground">targets</span>
          </span>
          <span>
            <strong className="font-medium">
              {targets.reduce((count, item) => count + item.services.length, 0)}
            </strong>{" "}
            <span className="text-muted-foreground">services</span>
          </span>
          <span className="flex items-center gap-2 text-muted-foreground">
            <span
              className={cn(
                "size-1.5 rounded-full",
                sync?.failed
                  ? "bg-destructive"
                  : busy
                    ? "bg-weld"
                    : "bg-overdye"
              )}
            />
            {sync?.failed
              ? "Sync needs attention"
              : busy
                ? `Syncing ${sync.target}`
                : "All targets in sync"}
          </span>
        </div>

        <CloudTabs value={tab} onChange={setTab}>
          <MockCloudInfrastructure
            key={resetKey}
            tab={tab}
            failure={simulateFailure}
            onPoolsChange={setPools}
          />
          {tab === "Targets" && (
            <div className="grid items-start gap-5 lg:grid-cols-[280px_minmax(0,1fr)]">
              <section
                aria-label="Cloud targets"
                className="grid min-w-0 gap-3"
              >
                <div className="flex items-center justify-between">
                  <h2 className="text-sm font-semibold">Targets</h2>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setAdding(true)}
                  >
                    <PlusIcon /> Add target
                  </Button>
                </div>
                <Select
                  value={selected}
                  onValueChange={(event) => setSelected(event ?? "")}
                >
                  <SelectTrigger
                    aria-label="Selected target"
                    className={cn("w-full min-w-0", "lg:hidden")}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {targets.map((item) => (
                      <SelectItem key={item.name} value={item.name}>
                        {item.name} · {item.provider}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Input
                  className="hidden lg:block"
                  aria-label="Filter targets"
                  placeholder="Filter targets..."
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
                <div className="hidden gap-1 lg:grid">
                  {visible.map((item) => (
                    <Button
                      variant="ghost"
                      key={item.name}
                      type="button"
                      aria-pressed={selected === item.name}
                      onClick={() => setSelected(item.name)}
                      className={cn(
                        "flex w-full items-center gap-3 rounded-lg border border-transparent p-3 text-left transition-colors outline-none hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring",
                        selected === item.name && "border-border bg-muted/60"
                      )}
                    >
                      <CloudGlyph cloud={item.cloud} className="size-8" />
                      <span className="min-w-0 flex-1">
                        <span className="block text-sm font-medium">
                          {item.name}
                        </span>
                        <span className="block text-xs text-muted-foreground">
                          {item.provider} · {item.services.length} services
                        </span>
                      </span>
                      {sync?.target === item.name && sync.failed ? (
                        <TriangleAlertIcon className="size-4 text-destructive" />
                      ) : sync?.target === item.name && busy ? (
                        <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" />
                      ) : (
                        <span className="size-1.5 rounded-full bg-overdye" />
                      )}
                    </Button>
                  ))}
                  {visible.length === 0 && (
                    <ZeroState variant="inline" className="px-2">
                      <ZeroStateHeader>
                        <ZeroStateMedia variant="icon">
                          <CloudIcon />
                        </ZeroStateMedia>
                        <ZeroStateTitle>No matching targets</ZeroStateTitle>
                        <ZeroStateDescription>
                          Try another name or provider.
                        </ZeroStateDescription>
                      </ZeroStateHeader>
                      <ZeroStateContent>
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => setQuery("")}
                        >
                          Clear filter
                        </Button>
                      </ZeroStateContent>
                    </ZeroState>
                  )}
                </div>
                <p className="px-3 text-xs leading-relaxed text-muted-foreground">
                  Services use a target name. Change its account here to move
                  them together.
                </p>
              </section>

              <Card
                size="sm"
                role="region"
                aria-label={`${target.name} details`}
                className="min-w-0 gap-0 py-0"
              >
                <div className="flex flex-wrap items-center justify-between gap-3 border-b px-5 py-4">
                  <div className="flex items-center gap-2">
                    <h2 className="font-semibold">{target.name}</h2>
                    <Badge variant="outline">{target.provider}</Badge>
                  </div>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={busy || !!sync?.failed}
                    onClick={openSwitch}
                  >
                    Switch account
                  </Button>
                </div>
                <div className="grid gap-5 p-5">
                  <dl className="grid gap-4 sm:grid-cols-2">
                    <div>
                      <dt className="mb-2 text-xs text-muted-foreground">
                        {target.services.length
                          ? "Serving account"
                          : "Selected account"}
                      </dt>
                      <dd className="flex items-center gap-2 text-sm font-medium break-all">
                        <CloudGlyph cloud={target.cloud} />
                        {target.account}
                      </dd>
                    </div>
                    <div>
                      <dt className="mb-2 text-xs text-muted-foreground">
                        Future releases
                      </dt>
                      <dd className="flex items-center gap-2 text-sm font-medium break-all">
                        {targetSync?.to ?? target.account}
                        {targetSync && !complete && (
                          <Badge variant="secondary">
                            {targetSync.failed ? "Paused" : "Queued"}
                          </Badge>
                        )}
                      </dd>
                    </div>
                  </dl>

                  {targetSync && (
                    <div
                      role="status"
                      aria-live="polite"
                      className={cn(
                        "rounded-lg bg-muted/50 p-4",
                        targetSync.failed && "bg-destructive/5"
                      )}
                    >
                      <div className="flex items-start gap-2">
                        {targetSync.failed ? (
                          <TriangleAlertIcon className="mt-0.5 size-4 shrink-0 text-destructive" />
                        ) : complete ? (
                          <CheckIcon className="mt-0.5 size-4 shrink-0 text-overdye" />
                        ) : (
                          <LoaderCircleIcon className="mt-0.5 size-4 shrink-0 animate-spin motion-reduce:animate-none" />
                        )}
                        <div className="min-w-0 flex-1">
                          <p className="text-sm font-medium">
                            {targetSync.failed
                              ? "Readiness check failed"
                              : complete
                                ? "Account switched"
                                : "Moving your current release"}
                          </p>
                          <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
                            {targetSync.failed
                              ? `${targetSync.from} is still serving. The desired destination is saved. Verify the failure before retrying or reviewing recovery.`
                              : complete
                                ? target.services.length
                                  ? `Current and future releases now use ${targetSync.to}.`
                                  : `Future releases will use ${targetSync.to}. No services needed redeployment.`
                                : `${targetSync.from} → ${targetSync.to}. Your deployed source revision stays the same. Desired and applied placement remain separate until readiness and routing succeed.`}
                          </p>
                        </div>
                      </div>
                      {!complete && (
                        <ol className="mt-3 grid gap-2">
                          {steps.map((label, index) => (
                            <li
                              key={label}
                              className={cn(
                                "flex items-center gap-2 text-xs",
                                index > targetSync.step
                                  ? "text-muted-foreground"
                                  : "text-foreground"
                              )}
                            >
                              <span
                                className={cn(
                                  "flex size-4 shrink-0 items-center justify-center rounded-full",
                                  index < targetSync.step
                                    ? "bg-overdye/10 text-overdye"
                                    : "bg-muted"
                                )}
                              >
                                {index < targetSync.step ? (
                                  <CheckIcon className="size-3" />
                                ) : (
                                  <span className="size-1 rounded-full bg-current" />
                                )}
                              </span>
                              {label}
                              {index === targetSync.step && (
                                <span className="ml-auto">
                                  {targetSync.failed ? "Failed" : "In progress"}
                                </span>
                              )}
                            </li>
                          ))}
                        </ol>
                      )}
                      {targetSync.failed && (
                        <div className="mt-3 flex flex-wrap gap-2">
                          <Button
                            size="sm"
                            onClick={() =>
                              setSync({
                                ...targetSync,
                                failed: false,
                                simulateFailure: false,
                              })
                            }
                          >
                            Retry sync
                          </Button>
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => setSync(null)}
                          >
                            Revert to {targetSync.from}
                          </Button>
                        </div>
                      )}
                    </div>
                  )}

                  <div>
                    <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
                      <h3 className="text-sm font-medium">
                        Services using this target
                      </h3>
                      {target.commit && (
                        <span className="flex items-center gap-1 text-xs text-muted-foreground">
                          <GitCommitHorizontalIcon className="size-3.5" />
                          {target.commit}
                        </span>
                      )}
                    </div>
                    {target.services.length > 0 ? (
                      <div className="divide-y">
                        {target.services.map((service) => (
                          <div
                            key={service}
                            className="flex items-center justify-between gap-2 py-3 text-sm"
                          >
                            <span>{service}</span>
                            <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                              <span className="size-1.5 rounded-full bg-overdye" />
                              Serving
                            </span>
                          </div>
                        ))}
                      </div>
                    ) : (
                      <ZeroState variant="inline" className="px-0">
                        <ZeroStateHeader>
                          <ZeroStateMedia variant="icon">
                            <CloudIcon />
                          </ZeroStateMedia>
                          <ZeroStateTitle>
                            Ready for your first service
                          </ZeroStateTitle>
                          <ZeroStateDescription>
                            Assign a service to this target in your release
                            configuration.
                          </ZeroStateDescription>
                        </ZeroStateHeader>
                        <ZeroStateContent>
                          <code className="rounded bg-muted px-2 py-1 text-xs">
                            cloud_target: {target.name}
                          </code>
                        </ZeroStateContent>
                      </ZeroState>
                    )}
                  </div>
                  <p className="border-t pt-3 text-xs leading-relaxed text-muted-foreground">
                    Switching accounts redeploys these services first. The
                    previous deployment stays available until the new one is
                    ready.
                  </p>
                </div>
              </Card>
            </div>
          )}
        </CloudTabs>
      </Page>

      <Dialog open={switching} onOpenChange={setSwitching}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Switch {target.name} account</DialogTitle>
            <DialogDescription>
              Redeploy the current release and use the new account for future
              releases.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-wrap items-center gap-2 rounded-lg bg-muted/50 p-3 text-sm">
            <CloudGlyph cloud={target.cloud} />
            <span>{target.account}</span>
            <ArrowRightIcon className="size-4 text-muted-foreground" />
            <span className="font-medium break-all">
              {chosen || "New account"}
            </span>
          </div>
          <FormField className="grid gap-2 text-sm font-medium">
            <FieldLabel className="contents">
              New {target.provider} account
              <Select
                value={destination}
                onValueChange={(event) => setDestination(event ?? "")}
              >
                <SelectTrigger
                  aria-label={`New ${target.provider} account`}
                  className={"w-full min-w-0"}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {target.accounts
                    .filter((account) => account !== target.account)
                    .map((account) => (
                      <SelectItem key={account} value={account}>
                        {account}
                      </SelectItem>
                    ))}
                  <SelectItem value="new">
                    Connect another account...
                  </SelectItem>
                </SelectContent>
              </Select>
            </FieldLabel>
          </FormField>
          {destination === "new" && (
            <FormField className="grid gap-2 text-sm font-medium">
              <FieldLabel className="contents">
                Account name
                <Input
                  value={newAccount}
                  onChange={(event) => setNewAccount(event.target.value)}
                  placeholder="e.g. Forge production"
                  maxLength={60}
                />
                <span className="text-xs font-normal text-muted-foreground">
                  This mock adds a sample connection. No credentials required.
                </span>
              </FieldLabel>
            </FormField>
          )}
          <div className="grid gap-2 text-sm">
            <p className="font-medium">What changes</p>
            <p className="text-muted-foreground">
              {target.services.length
                ? `${target.services.join(" and ")} will redeploy from ${target.commit}.`
                : "This target has no deployed services yet."}
            </p>
            <p className="text-muted-foreground">
              Future releases will use {chosen || "the selected account"}.
            </p>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setSwitching(false)}>
              Cancel
            </Button>
            <Button
              disabled={!chosen || chosen === target.account}
              onClick={startSync}
            >
              Switch and sync
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={adding} onOpenChange={setAdding}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add target</DialogTitle>
            <DialogDescription>
              Give a group of services a stable destination.
            </DialogDescription>
          </DialogHeader>
          <FormField className="grid gap-2 text-sm font-medium">
            <FieldLabel className="contents">
              Target name
              <Input
                value={newName}
                onChange={(event) => setNewName(event.target.value)}
                placeholder="e.g. preview"
                maxLength={40}
              />
              <span className="text-xs font-normal text-muted-foreground">
                Lowercase letters, numbers and hyphens. Start with a letter.
              </span>
            </FieldLabel>
          </FormField>
          <FormField className="grid gap-2 text-sm font-medium">
            <FieldLabel className="contents">
              Provider
              <Select
                value={newProvider}
                onValueChange={(event) => setNewProvider(event ?? "")}
              >
                <SelectTrigger
                  aria-label="Provider"
                  className={"w-full min-w-0"}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {initialTargets.map((item) => (
                    <SelectItem key={item.provider} value={item.provider}>
                      {item.provider}
                    </SelectItem>
                  ))}
                  {pools.length > 0 && (
                    <SelectItem value="Server pool">Server pool</SelectItem>
                  )}
                </SelectContent>
              </Select>
            </FieldLabel>
          </FormField>
          <p className="text-sm text-muted-foreground">
            Connected account:{" "}
            {newProvider === "Server pool"
              ? pools[0]
              : initialTargets.find((item) => item.provider === newProvider)
                  ?.account}
          </p>
          {targets.some((item) => item.name === newName.trim()) && (
            <Alert variant="destructive" className="text-xs text-destructive">
              <AlertDescription>
                That target name is already in use.
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setAdding(false)}>
              Cancel
            </Button>
            <Button
              disabled={
                !/^[a-z][a-z0-9-]*$/.test(newName.trim()) ||
                targets.some((item) => item.name === newName.trim())
              }
              onClick={addTarget}
            >
              Add target
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
