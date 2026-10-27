import { Radio } from "@base-ui/react/radio"
import { RadioGroup } from "@base-ui/react/radio-group"
import { CheckIcon, CopyIcon, PlusIcon } from "lucide-react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useApiKeysCreate,
  useApiKeysList,
  useApiKeysRevoke,
} from "@/client/forge/loomplane/hooks"
import type { APIKey, CreatedKey } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { ApiKeysIcon } from "@/components/icons"
import { Page, PageHeading } from "@/components/page"
import { Panel } from "@/components/panel"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { formatDate } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { cn } from "@/lib/utils"

const accessChoices = [
  {
    label: "Read only",
    value: "read",
    description:
      "Looks at projects, plans and deployments, and changes nothing.",
  },
  {
    label: "Read and write",
    value: "write",
    description: "Can plan and deploy, as far as your role allows.",
  },
]

const lifetimes = [
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
  { days: 180, label: "180 days" },
  { days: 366, label: "A year" },
]

/** ApiKeys lists the keys the CLI, CI and scripts use, and makes new ones. */
export function ApiKeys() {
  const { session, can } = useOrganization()
  const keys = useQuery(useApiKeysList)
  const [creating, setCreating] = useState(false)
  const admin = can("organization:administer")

  return (
    <>
      <SiteHeader crumbs={[{ label: "API keys" }]} />
      <Page className="max-w-[980px]">
        <PageHeading
          title="API keys"
          description={
            admin
              ? "Keys for the CLI, CI and scripts. Each acts as the person who made it, with their role, here only. You see everyone's."
              : "Keys for the CLI, CI and scripts. Each acts as you, with your role, in this organization only."
          }
          actions={
            <Button onClick={() => setCreating(true)}>
              <PlusIcon />
              New key
            </Button>
          }
        />

        {keys.status === "error" && (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(keys.error, "API keys couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        )}
        {!keys.data && keys.status !== "error" && (
          <Skeleton className="h-40 w-full rounded-xl" />
        )}
        {keys.data && keys.data.keys.length === 0 && (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <ApiKeysIcon />
              </EmptyMedia>
              <EmptyTitle>No keys yet</EmptyTitle>
              <EmptyDescription>
                Make one to use the CLI or call the API from CI.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button onClick={() => setCreating(true)}>New key</Button>
            </EmptyContent>
          </Empty>
        )}
        {keys.data && keys.data.keys.length > 0 && (
          <Card className="py-0">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead>Key</TableHead>
                  <TableHead>Access</TableHead>
                  <TableHead className="hidden md:table-cell">
                    Last used
                  </TableHead>
                  <TableHead className="hidden md:table-cell">
                    Expires
                  </TableHead>
                  <TableHead className="w-0">
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {keys.data.keys.map((k) => (
                  <KeyRow
                    key={k.id}
                    apiKey={k}
                    mine={k.createdBy === session.userId}
                  />
                ))}
              </TableBody>
            </Table>
          </Card>
        )}
        <Panel className="gap-2">
          <h2 className="text-[13.5px] font-semibold">Using a key</h2>
          <code className="block rounded-lg bg-muted px-3 py-2.5 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap">
            curl -H &quot;Authorization: Bearer &lt;key&gt;&quot;{" "}
            {window.location.origin}/api/...
          </code>
        </Panel>
      </Page>
      <CreateKeyDialog open={creating} onOpenChange={setCreating} />
    </>
  )
}

function KeyRow({ apiKey: k, mine }: { apiKey: APIKey; mine: boolean }) {
  const revoke = useMutation(useApiKeysRevoke)
  const invalidate = useInvalidate()
  const active = k.state === "active"

  const doRevoke = async () => {
    try {
      await revoke.mutateAsync({ path: { id: k.id } })
      toast.success(`${k.name} is revoked`)
    } catch (err) {
      toast.error(messageOf(err, "The key wasn't revoked."))
    } finally {
      invalidate(useApiKeysList)
    }
  }

  return (
    <TableRow>
      <TableCell>
        <div className="text-[13.5px] font-medium">{k.name}</div>
        <div className="font-mono text-[11.5px] text-muted-foreground">
          lp_live_…{k.hint}
          {!mine && " · someone else's"}
        </div>
      </TableCell>
      <TableCell>
        {active ? (
          <Badge
            variant="secondary"
            className={cn(
              k.access === "write" && "bg-brand-tint text-brand-ink"
            )}
          >
            {k.access === "write" ? "Read and write" : "Read only"}
          </Badge>
        ) : (
          <Badge variant="outline" className="font-normal capitalize">
            {k.state}
          </Badge>
        )}
      </TableCell>
      <TableCell className="hidden text-[12.5px] text-muted-foreground md:table-cell">
        {k.lastUsedAt ? formatDate(k.lastUsedAt) : "Never"}
      </TableCell>
      <TableCell className="hidden text-[12.5px] text-muted-foreground md:table-cell">
        {k.expiresAt ? formatDate(k.expiresAt) : "Never"}
      </TableCell>
      <TableCell>
        {active && (
          <AlertDialog>
            <AlertDialogTrigger
              render={
                <Button
                  variant="outline"
                  size="sm"
                  className="text-destructive hover:text-destructive"
                  disabled={revoke.isPending}
                >
                  Revoke
                </Button>
              }
            />
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>Revoke {k.name}?</AlertDialogTitle>
                <AlertDialogDescription>
                  Anything using it stops working at once. You can't undo this;
                  make a new key instead.
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Keep it</AlertDialogCancel>
                <AlertDialogAction
                  variant="destructive"
                  onClick={() => void doRevoke()}
                >
                  Revoke
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        )}
      </TableCell>
    </TableRow>
  )
}

function CreateKeyDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const create = useMutation(useApiKeysCreate)
  const invalidate = useInvalidate()
  const [name, setName] = useState("")
  const [access, setAccess] = useState("read")
  const [days, setDays] = useState("90")
  const [secret, setSecret] = useState("")
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState("")

  const close = (next: boolean) => {
    if (!next) {
      setName("")
      setAccess("read")
      setDays("90")
      setSecret("")
      setCopied(false)
      setError("")
    }
    onOpenChange(next)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    try {
      const out = (await create.mutateAsync({
        body: { name: name.trim(), access, expiresInDays: Number(days) },
      })) as CreatedKey
      setSecret(out.secret)
      invalidate(useApiKeysList)
    } catch (err) {
      setError(messageOf(err, "The key wasn't made."))
    }
  }

  const copy = async () => {
    await navigator.clipboard.writeText(secret)
    setCopied(true)
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="sm:max-w-[520px]">
        {secret ? (
          <>
            <DialogHeader>
              <DialogTitle>Copy your key now</DialogTitle>
              <DialogDescription>
                This is the only time you'll see it. Loomplane keeps a hash, so
                if you lose it, revoke it and make another.
              </DialogDescription>
            </DialogHeader>
            <div className="flex gap-1.5">
              <Input
                readOnly
                value={secret}
                aria-label="Your new key"
                className="h-9 border-transparent bg-muted font-mono text-[12.5px]"
                onFocus={(e) => e.target.select()}
              />
              <Button
                variant="outline"
                className="h-9"
                onClick={() => void copy()}
              >
                {copied ? <CheckIcon /> : <CopyIcon />}
                {copied ? "Copied" : "Copy"}
              </Button>
            </div>
            <p className="text-muted-foreground">
              Use it with the CLI:{" "}
              <code className="font-mono text-xs">
                loomctl login --url {window.location.origin} --key -
              </code>
              , or send it as{" "}
              <code className="font-mono text-xs">
                Authorization: Bearer &lt;key&gt;
              </code>
              .
            </p>
            <DialogFooter>
              <Button onClick={() => close(false)}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={(e) => void submit(e)} className="grid gap-5">
            <DialogHeader>
              <DialogTitle>New API key</DialogTitle>
              <DialogDescription>
                It acts as you, in this organization, and can never do more than
                your role allows.
              </DialogDescription>
            </DialogHeader>
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="key-name">Name</FieldLabel>
                <Input
                  id="key-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="GitHub Actions, my laptop"
                  maxLength={80}
                  required
                />
              </Field>
              <Field>
                <FieldLabel id="key-access">Access</FieldLabel>
                <RadioGroup
                  value={access}
                  onValueChange={(v) => setAccess(v as string)}
                  aria-labelledby="key-access"
                  className="grid gap-2 sm:grid-cols-2"
                >
                  {accessChoices.map((a) => (
                    <Radio.Root
                      key={a.value}
                      value={a.value}
                      nativeButton
                      render={<button type="button" />}
                      className={cn(
                        "grid content-start gap-0.5 rounded-[11px] border px-3 py-2.5 text-left transition-colors",
                        access === a.value
                          ? "border-brand-line bg-brand-soft"
                          : "border-border bg-card hover:border-foreground/25"
                      )}
                    >
                      <span className="text-[13px] font-semibold">
                        {a.label}
                      </span>
                      <span className="text-xs leading-snug text-muted-foreground">
                        {a.description}
                      </span>
                    </Radio.Root>
                  ))}
                </RadioGroup>
              </Field>
              <Field>
                <FieldLabel>Expires after</FieldLabel>
                <Select
                  items={lifetimes.map((l) => ({
                    label: l.label,
                    value: String(l.days),
                  }))}
                  value={days}
                  onValueChange={(v) => setDays(v ?? "90")}
                >
                  <SelectTrigger aria-label="Expires after" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {lifetimes.map((l) => (
                      <SelectItem key={l.days} value={String(l.days)}>
                        {l.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </FieldGroup>
            {error && (
              <Alert variant="destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => close(false)}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={create.isPending}>
                {create.isPending && <Spinner />}
                Make the key
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
