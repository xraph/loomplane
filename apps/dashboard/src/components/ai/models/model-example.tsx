// What a linked service writes to call the model. Every snippet reads
// MODEL_URL and MODEL_API_KEY from the environment: the key itself is never
// shown here.
import { CheckIcon, CopyIcon } from "lucide-react"
import { useState } from "react"

import { useManifestsCurrent } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import type { ModelProps } from "@/components/ai/models/model-overview"
import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { snippets, taskTitles } from "@/lib/model-examples"
import { exampleTasks, servedNameOf } from "@/lib/models"
import { Link } from "@/lib/router"

export function ModelExample({ status, projectId }: ModelProps) {
  const manifest = useQuery(useManifestsCurrent, { path: { id: projectId } })
  const resources = manifest.data?.manifest?.resources as
    Record<string, { settings?: Record<string, unknown> }> | undefined
  const served = servedNameOf(
    status.entry.id,
    resources?.[status.resource]?.settings
  )
  const tasks = exampleTasks(status.entry.tasks)
  return (
    <div className="grid min-w-0 gap-6">
      <div className="grid gap-1">
        <p className="text-[13.5px] text-pretty">
          Services that link to{" "}
          <span className="font-mono text-[12.5px]">{status.resource}</span> get{" "}
          <span className="font-mono text-[12.5px]">MODEL_URL</span> and{" "}
          <span className="font-mono text-[12.5px]">MODEL_API_KEY</span>.
        </p>
        <p className="text-[12.5px] text-muted-foreground">
          The model is called{" "}
          <span className="font-mono text-[12px]">{served}</span>. Loomplane
          never shows the key. Your service reads it from its environment.
        </p>
      </div>
      {tasks.map((task) => (
        <Task
          key={task}
          task={task}
          served={served}
          entryId={
            status.entry.id && !status.entry.id.startsWith("hf:")
              ? status.entry.id
              : undefined
          }
          multiple={tasks.length > 1}
        />
      ))}
    </div>
  )
}

function Task({
  task,
  served,
  entryId,
  multiple,
}: {
  task: Parameters<typeof snippets>[0]
  served: string
  /** The registry entry, for its card; none for a pasted repo. */
  entryId?: string
  multiple: boolean
}) {
  const list = snippets(task, served)
  return (
    <section aria-label={taskTitles[task]} className="grid min-w-0 gap-2">
      {multiple && (
        <h3 className="text-[13px] font-semibold">{taskTitles[task]}</h3>
      )}
      {task === "chat" && (
        <p className="text-[12.5px] text-muted-foreground">
          It speaks the OpenAI API, so the{" "}
          <span className="font-mono">openai</span> package works with{" "}
          <span className="font-mono">
            base_url = $MODEL_URL + &quot;/v1&quot;
          </span>
          .
        </p>
      )}
      {task === "embedding" && (
        <p className="text-[12.5px] text-muted-foreground">
          Many embedding models want a prefix on each query, such as{" "}
          <span className="font-mono">&quot;query: &quot;</span>, and some want
          another on documents. Add it to the text you send. The model&apos;s
          card says which{entryId ? ": " : "."}
          {entryId && (
            <Link
              to="/ai/models/$"
              params={{ _splat: entryId }}
              className="font-medium text-primary hover:underline"
            >
              open its card
            </Link>
          )}
          {entryId && "."}
        </p>
      )}
      {task === "rerank" && (
        <p className="text-[12.5px] text-muted-foreground">
          Reranking uses the engine&apos;s own{" "}
          <span className="font-mono">POST $MODEL_URL/rerank</span>.
        </p>
      )}
      {task === "speech-to-text" && (
        <p className="text-[12.5px] text-muted-foreground">
          Transcription is{" "}
          <span className="font-mono">
            POST $MODEL_URL/v1/audio/transcriptions
          </span>
          .
        </p>
      )}
      <Tabs defaultValue={list[0].id} className="min-w-0 gap-2">
        <TabsList variant="line">
          {list.map((s) => (
            <TabsTrigger key={s.id} value={s.id}>
              {s.label}
            </TabsTrigger>
          ))}
        </TabsList>
        {list.map((s) => (
          <TabsContent key={s.id} value={s.id} className="min-w-0">
            <Code label={`${taskTitles[task]} in ${s.label}`} code={s.code} />
          </TabsContent>
        ))}
      </Tabs>
    </section>
  )
}

function Code({ label, code }: { label: string; code: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <div className="grid min-w-0 overflow-hidden rounded-xl bg-subtle ring-1 ring-border">
      <div className="flex justify-end border-b px-2 py-1.5">
        <Button
          size="sm"
          variant="ghost"
          aria-label={`Copy ${label}`}
          onClick={() => {
            void navigator.clipboard
              ?.writeText(code)
              .then(() => {
                setCopied(true)
                window.setTimeout(() => setCopied(false), 1500)
              })
              .catch(() => undefined)
          }}
        >
          {copied ? (
            <CheckIcon data-icon="inline-start" />
          ) : (
            <CopyIcon data-icon="inline-start" />
          )}
          {copied ? "Copied" : "Copy"}
        </Button>
      </div>
      <pre
        tabIndex={0}
        aria-label={label}
        className="max-w-full overflow-x-auto p-4 font-mono text-[12.5px] leading-relaxed"
      >
        <code>{code}</code>
      </pre>
    </div>
  )
}
