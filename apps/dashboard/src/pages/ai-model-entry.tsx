// One model in the registry: what it is and under which license, its
// quantizations, where each fits and how sure Loomplane is of it, and its
// card from Hugging Face. Deploy opens the deploy dialog.
import { ArrowUpRightIcon, LockIcon, SearchIcon } from "lucide-react"
import { useState } from "react"

import { statusOf } from "@/client/errors"
import {
  useAiModelsCard,
  useAiModelsEntry,
} from "@/client/forge/loomplane/hooks"
import type {
  EntryDetail,
  QuantizationView,
} from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { DeployModelDialog } from "@/components/ai/models/deploy-model-dialog"
import { ModelMarkdown } from "@/components/ai/models/model-markdown"
import { VerificationBadge } from "@/components/ai/models/verification-badge"
import { Page, PageHeading, SectionHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { gpuTitle } from "@/lib/gpu"
import {
  engineName,
  formatContext,
  formatParams,
  gpuMemoryOf,
  runsOnOf,
  taskLabel,
  weightSize,
} from "@/lib/models"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"

export function AIModelEntry({ id }: { id: string }) {
  const entry = useQuery(useAiModelsEntry, { query: { id } })
  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Models", to: "/ai/models" },
          { label: entry.data?.title ?? id },
        ]}
      />
      <Page className="gap-7">
        {entry.status === "error" ? (
          <EntryError error={entry.error} retry={entry.refetch} />
        ) : !entry.data ? (
          <div data-testid="entry-loading" className="grid gap-4">
            <Skeleton className="h-4 w-24" />
            <Skeleton className="h-7 w-80 max-w-full" />
            <Skeleton className="h-4 w-full max-w-xl" />
            <Skeleton className="h-40 w-full rounded-xl" />
          </div>
        ) : (
          <Entry entry={entry.data} />
        )}
      </Page>
    </>
  )
}

function EntryError({
  error,
  retry,
}: {
  error: unknown
  retry: () => Promise<unknown>
}) {
  const status = statusOf(error)
  if (status === 403)
    return (
      <ZeroState preview={false}>
        <ZeroStateHeader>
          <ZeroStateMedia variant="icon">
            <LockIcon />
          </ZeroStateMedia>
          <ZeroStateTitle>Models are restricted</ZeroStateTitle>
          <ZeroStateDescription>
            Ask a workspace admin for access to AI in this workspace.
          </ZeroStateDescription>
        </ZeroStateHeader>
      </ZeroState>
    )
  if (status === 404)
    return (
      <ZeroState preview={false}>
        <ZeroStateHeader>
          <ZeroStateMedia variant="icon">
            <SearchIcon />
          </ZeroStateMedia>
          <ZeroStateTitle>No such model</ZeroStateTitle>
          <ZeroStateDescription>
            The registry doesn&apos;t have this model. It may have been renamed,
            or the link is wrong.
          </ZeroStateDescription>
        </ZeroStateHeader>
        <ZeroStateContent>
          <Button
            variant="outline"
            nativeButton={false}
            render={<Link to="/ai/models" />}
          >
            Browse the registry
          </Button>
        </ZeroStateContent>
      </ZeroState>
    )
  return (
    <QueryError
      error={error}
      fallback="The model couldn't be loaded."
      retry={retry}
    />
  )
}

function Entry({ entry }: { entry: EntryDetail }) {
  const { can } = useOrganization()
  const editor = can("ai:edit")
  const [tab, setTab] = useState(entry.quantizations[0]?.id ?? "")
  const [deploying, setDeploying] = useState(false)
  const facts = [
    formatParams(entry.parameters),
    formatContext(entry.contextLength),
    ...entry.tasks.map(taskLabel),
  ]
    .filter(Boolean)
    .join(" · ")
  return (
    <>
      <PageHeading
        eyebrow={
          <p className="text-[12.5px] font-medium text-muted-foreground">
            {entry.publisher}
          </p>
        }
        title={entry.title}
        description={entry.summary}
        actions={
          <>
            <Button
              variant="outline"
              nativeButton={false}
              render={
                <a
                  href={entry.huggingFaceUrl}
                  target="_blank"
                  rel="noreferrer"
                />
              }
            >
              Hugging Face
              <ArrowUpRightIcon data-icon="inline-end" />
            </Button>
            <Button
              disabled={!editor}
              title={
                editor
                  ? undefined
                  : "Ask a workspace admin for permission to deploy models."
              }
              onClick={() => setDeploying(true)}
            >
              Deploy
            </Button>
          </>
        }
      >
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-2 text-[12.5px]">
          <VerificationBadge
            status={entry.verification}
            gpuTitle={entry.testedOn[0]}
          />
          {entry.gated && (
            <span className="inline-flex items-center gap-1 font-medium">
              <LockIcon aria-hidden="true" className="size-3.5" />
              Gated
            </span>
          )}
          <span className="font-mono text-[12px] text-muted-foreground tabular-nums">
            {facts}
          </span>
        </div>
      </PageHeading>

      <License entry={entry} />

      {entry.fitsNotice && (
        <p role="status" className="text-[12.5px] text-muted-foreground">
          {entry.fitsNotice} Each quantization still says where it fits.
        </p>
      )}

      <section className="grid min-w-0 gap-3">
        <SectionHeading
          title="Quantizations"
          description="The weights Loomplane can serve, each pinned to a commit."
        />
        <Card className="py-0">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>ID</TableHead>
                <TableHead>Engine</TableHead>
                <TableHead className="text-right">Weights</TableHead>
                <TableHead>Min GPU memory</TableHead>
                <TableHead>Revision</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {entry.quantizations.map((q) => (
                <TableRow key={q.id}>
                  <TableCell className="font-mono text-[12.5px] font-medium">
                    {q.id}
                  </TableCell>
                  <TableCell>{engineName(q.engine)}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {weightSize(q.weightBytes)}
                  </TableCell>
                  <TableCell>{minGpuLabel(q)}</TableCell>
                  <TableCell>
                    <Revision entry={entry} quantization={q} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      </section>

      <section className="grid min-w-0 gap-3">
        <SectionHeading
          title="Where it fits"
          description="Tested means Loomplane's qualification deployed and served it there. Estimated is the memory math only."
        />
        <Tabs
          value={tab}
          onValueChange={(v) => setTab(String(v))}
          className="min-w-0"
        >
          <div className="max-w-full overflow-x-auto">
            <TabsList variant="line" aria-label="Quantization">
              {entry.quantizations.map((q) => (
                <TabsTrigger key={q.id} value={q.id} className="font-mono">
                  {q.id}
                </TabsTrigger>
              ))}
            </TabsList>
          </div>
          {entry.quantizations.map((q) => (
            <TabsContent key={q.id} value={q.id} className="min-w-0 pt-3">
              <Fits quantization={q} />
            </TabsContent>
          ))}
        </Tabs>
      </section>

      <section className="grid min-w-0 gap-3">
        <SectionHeading title="Model card" />
        <ModelCardSection id={entry.id} />
      </section>

      {deploying && (
        <DeployModelDialog
          entryId={entry.id}
          detail={entry}
          onClose={() => setDeploying(false)}
          onPlanned={() => setDeploying(false)}
        />
      )}
    </>
  )
}

function License({ entry }: { entry: EntryDetail }) {
  const l = entry.license
  const known = l.id !== "unknown"
  return (
    <section
      aria-label="License"
      className="grid gap-1 rounded-xl bg-card p-4 ring-1 ring-border"
    >
      <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[13.5px]">
        <span className="font-medium">
          {known ? l.title : "License unknown"}
        </span>
        {known && (
          <span className="text-muted-foreground">
            {l.commercial ? "Commercial use allowed" : "Not for commercial use"}
          </span>
        )}
        {l.url && (
          <a
            href={l.url}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-0.5 text-primary hover:underline"
          >
            Read it
            <ArrowUpRightIcon aria-hidden="true" className="size-3.5" />
          </a>
        )}
      </p>
      {l.note && (
        <p className="text-[12.5px] text-pretty text-muted-foreground">
          {l.note}
        </p>
      )}
      {entry.gated && (
        <p className="text-[12.5px] text-pretty text-muted-foreground">
          Hugging Face asks you to accept the license before you download it.
          The workspace&apos;s Hugging Face token needs an account that has.
        </p>
      )}
    </section>
  )
}

/** minGpuLabel is the GPU memory one GPU needs, or that it runs on CPU. */
function minGpuLabel(q: QuantizationView): string {
  const where = runsOnOf(q.engine)
  if (!where.includes("gpu")) return "Runs on CPU"
  const gib = gpuMemoryOf(q)
  const cpu = where.includes("cpu") ? ", or CPU" : ""
  return gib ? `${gib} GB${cpu}` : `unknown${cpu}`
}

function Revision({
  entry,
  quantization: q,
}: {
  entry: EntryDetail
  quantization: QuantizationView
}) {
  const sha = q.revision || entry.revision
  let base = "https://huggingface.co"
  let repo = q.repo ?? ""
  try {
    const u = new URL(entry.huggingFaceUrl)
    base = u.origin
    if (!repo) repo = u.pathname.replace(/^\/+/, "")
  } catch {
    // The page link is the server's; without it the commit can't be linked.
  }
  if (!sha) return <span className="text-muted-foreground">unknown</span>
  if (!repo)
    return <span className="font-mono text-[12px]">{sha.slice(0, 7)}</span>
  return (
    <a
      href={`${base}/${repo}/commit/${sha}`}
      target="_blank"
      rel="noreferrer"
      title={sha}
      className="font-mono text-[12px] text-primary hover:underline"
    >
      {sha.slice(0, 7)}
    </a>
  )
}

function Fits({ quantization: q }: { quantization: QuantizationView }) {
  if (q.fits.length === 0)
    return (
      <p className="text-[13px] text-muted-foreground">
        Loomplane has no fit for this quantization yet.
      </p>
    )
  return (
    <Card className="py-0">
      <Table aria-label={`Where ${q.id} fits`}>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>GPU</TableHead>
            <TableHead className="text-right">GPUs needed</TableHead>
            <TableHead className="text-right">Context that fits</TableHead>
            <TableHead>Status</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {q.fits.map((f) => {
            const cpu = f.gpuKind === "cpu"
            const off = f.status === "no-fit" || f.status === "failed"
            return (
              <TableRow key={f.gpuKind}>
                <TableCell className="font-medium whitespace-normal">
                  {cpu ? "CPU" : gpuTitle(f.gpuKind)}
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  {off ? "" : cpu ? "None" : f.need.gpus}
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  {off || !f.need.maxModelLen
                    ? ""
                    : `${f.need.maxModelLen.toLocaleString("en-US")} tokens`}
                </TableCell>
                <TableCell className="whitespace-normal">
                  <div className="grid gap-1">
                    <VerificationBadge status={f.status} fit={f} />
                    {off && (f.result?.detail || f.reason) && (
                      <span className="text-[12px] text-muted-foreground">
                        {f.result?.detail || f.reason}
                      </span>
                    )}
                  </div>
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </Card>
  )
}

function ModelCardSection({ id }: { id: string }) {
  const card = useQuery(useAiModelsCard, { query: { id } })
  if (card.status === "error")
    return (
      <QueryError
        error={card.error}
        fallback="The model card couldn't be loaded from Hugging Face."
        retry={card.refetch}
      />
    )
  if (!card.data)
    return (
      <div data-testid="card-loading" className="grid gap-2">
        <Skeleton className="h-4 w-2/3" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-5/6" />
      </div>
    )
  const c = card.data
  return (
    <div className="grid min-w-0 gap-3 rounded-xl bg-card p-4 ring-1 ring-border md:p-6">
      {c.note && (
        <p className="text-[12.5px] text-muted-foreground">{c.note}</p>
      )}
      {c.markdown.trim() ? (
        <ModelMarkdown markdown={c.markdown} />
      ) : (
        !c.note && (
          <p className="text-[13px] text-muted-foreground">
            This model has no card on Hugging Face.
          </p>
        )
      )}
      {c.sourceUrl && (
        <a
          href={c.sourceUrl}
          target="_blank"
          rel="noreferrer"
          className="inline-flex w-fit items-center gap-0.5 text-[12.5px] text-primary hover:underline"
        >
          Read it on Hugging Face
          <ArrowUpRightIcon aria-hidden="true" className="size-3.5" />
        </a>
      )}
    </div>
  )
}
