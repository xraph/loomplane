// Setting many variables at once: the ones an analysis found a service
// reading, or the lines of a pasted .env. Each row is checked, filled in
// and marked secret or not before anything is set.

import { DicesIcon, EyeIcon, EyeOffIcon } from "lucide-react"
import { useState } from "react"

import { messageOf } from "@/client/errors"
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
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import {
  Tabs,
  TabsContent,
  TabsCount,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import {
  type Detected,
  type ImportRow,
  rowsFromDetected,
  rowsFromPaste,
  whereRead,
} from "@/lib/env-import"
import { generatorFor, generators } from "@/lib/generate-value"
import { cn } from "@/lib/utils"

export type ImportSource = "detected" | "paste"

export interface ImportedVariable {
  key: string
  value: string
  secret: boolean
  environment: string
}

export interface VariableImportProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Where the variables are set, such as "for api" or "for the whole project". */
  where: string
  /** The environment being looked at. */
  environment: string
  /** What the analysis found the target reading. */
  found: Detected[]
  /** Keys already set for the target in the environment. */
  set: Set<string>
  /** The tab to open on. */
  source: ImportSource
  /** Sets one variable; a rejection is shown on its row. */
  submit: (v: ImportedVariable) => Promise<void>
  /** Called once, after a run that set at least one. */
  onDone: (count: number) => void
}

/** VariableImport sets several variables at once. */
export function VariableImport(props: VariableImportProps) {
  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className="sm:max-w-[860px]">
        {/* Mounted fresh each time it opens, so it starts from its props. */}
        {props.open && <ImportForm {...props} />}
      </DialogContent>
    </Dialog>
  )
}

function ImportForm({
  onOpenChange,
  where,
  environment,
  found,
  set,
  source,
  submit,
  onDone,
}: VariableImportProps) {
  const [tab, setTab] = useState<ImportSource>(source)
  const [detectedRows, setDetectedRows] = useState(() =>
    rowsFromDetected(found, set)
  )
  const [pasted, setPasted] = useState("")
  const [pastedRows, setPastedRows] = useState<ImportRow[]>([])
  const [problems, setProblems] = useState<{ line: number; message: string }[]>(
    []
  )
  const [applies, setApplies] = useState("")
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [saving, setSaving] = useState(false)

  const alreadySet = found.length - rowsFromDetected(found, set).length
  const rows = tab === "detected" ? detectedRows : pastedRows
  const setRows = tab === "detected" ? setDetectedRows : setPastedRows
  const chosen = rows.filter((r) => r.include)
  const emptySecrets = chosen.filter((r) => r.secret && r.value === "")

  const readPaste = (text: string) => {
    setPasted(text)
    const { rows, problems } = rowsFromPaste(text)
    // Keep what someone already changed on a row whose key is still there.
    setPastedRows((prior) =>
      rows.map((r) => {
        const was = prior.find((p) => p.key === r.key)
        return was && was.value === r.value ? was : r
      })
    )
    setProblems(problems)
  }

  const change = (key: string, patch: Partial<ImportRow>) =>
    setRows((list) => list.map((r) => (r.key === key ? { ...r, ...patch } : r)))

  const run = async () => {
    setSaving(true)
    const failed: Record<string, string> = {}
    const done = new Set<string>()
    // One at a time: each is its own write, and a secret's goes to Vault.
    for (const r of chosen) {
      try {
        await submit({
          key: r.key,
          value: r.value,
          secret: r.secret,
          environment: applies,
        })
        done.add(r.key)
      } catch (err) {
        failed[r.key] = messageOf(err, "It wasn't set.")
      }
    }
    setSaving(false)
    setErrors(failed)
    setRows((list) => list.filter((r) => !done.has(r.key)))
    if (done.size > 0) onDone(done.size)
    if (Object.keys(failed).length === 0) onOpenChange(false)
  }

  return (
    <div className="grid gap-4">
      <DialogHeader>
        <DialogTitle>Import variables {where}</DialogTitle>
        <DialogDescription>
          Check each one before it&apos;s set. Secrets are encrypted and never
          shown again, and generated values are made here in your browser.
        </DialogDescription>
      </DialogHeader>

      <Tabs value={tab} onValueChange={(v) => setTab(v as ImportSource)}>
        <TabsList variant="line">
          <TabsTrigger value="detected">
            Found by analysis
            <TabsCount>{detectedRows.length}</TabsCount>
          </TabsTrigger>
          <TabsTrigger value="paste">
            Paste a .env
            {pastedRows.length > 0 && (
              <TabsCount>{pastedRows.length}</TabsCount>
            )}
          </TabsTrigger>
        </TabsList>

        <TabsContent value="detected" className="grid gap-3 pt-2">
          {found.length === 0 ? (
            <p className="text-muted-foreground">
              The latest analysis found no variables read {where}. Analyze the
              repository again to look for them, or paste a .env instead.
            </p>
          ) : detectedRows.length === 0 ? (
            <p className="text-muted-foreground">
              Every variable the analysis found is already set in {environment}.
            </p>
          ) : (
            alreadySet > 0 && (
              <p className="text-muted-foreground">
                {alreadySet} more {alreadySet === 1 ? "is" : "are"} already set
                in {environment}, so{" "}
                {alreadySet === 1 ? "it isn't" : "they aren't"} listed.
              </p>
            )
          )}
        </TabsContent>

        <TabsContent value="paste" className="grid gap-2 pt-2">
          <Textarea
            aria-label=".env content"
            className="min-h-28 font-mono text-[12.5px]"
            placeholder={
              "DATABASE_URL=postgres://…\nSESSION_SECRET=…\n# comments are skipped"
            }
            value={pasted}
            onChange={(e) => readPaste(e.target.value)}
            spellCheck={false}
            autoFocus={source === "paste"}
          />
          {problems.length > 0 && (
            <ul className="grid gap-0.5 text-xs text-muted-foreground">
              {problems.map((p) => (
                <li key={`${p.line}-${p.message}`}>
                  Line {p.line}: {p.message}
                </li>
              ))}
            </ul>
          )}
        </TabsContent>
      </Tabs>

      {rows.length > 0 && (
        <div className="max-h-[46vh] overflow-y-auto rounded-lg border">
          <table className="w-full text-[13px]">
            <thead className="sticky top-0 bg-background text-left text-xs text-muted-foreground">
              <tr className="border-b">
                <th className="w-8 py-2 pl-3">
                  <Checkbox
                    aria-label="Set every one"
                    checked={chosen.length === rows.length}
                    indeterminate={
                      chosen.length > 0 && chosen.length < rows.length
                    }
                    onCheckedChange={(c) =>
                      setRows((list) => list.map((r) => ({ ...r, include: c })))
                    }
                  />
                </th>
                <th className="py-2 pr-3 font-medium">Key</th>
                <th className="py-2 pr-3 font-medium">Value</th>
                <th className="w-20 py-2 pr-3 font-medium">Secret</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <RowEditor
                  key={r.key}
                  row={r}
                  error={errors[r.key]}
                  onChange={(patch) => change(r.key, patch)}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {emptySecrets.length > 0 && (
        <Alert>
          <AlertDescription>
            {emptySecrets.map((r) => r.key).join(", ")}{" "}
            {emptySecrets.length === 1 ? "is a secret" : "are secrets"} with no
            value. Generate one, enter one, or untick{" "}
            {emptySecrets.length === 1 ? "it" : "them"}.
          </AlertDescription>
        </Alert>
      )}

      <DialogFooter className="items-center gap-2 sm:justify-between">
        <Select
          items={[
            { label: "Every environment", value: "" },
            { label: `${environment} only`, value: environment },
          ]}
          value={applies}
          onValueChange={(v) => setApplies(v as string)}
        >
          <SelectTrigger size="sm" aria-label="Applies in" className="w-48">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="">Every environment</SelectItem>
            <SelectItem value={environment}>{environment} only</SelectItem>
          </SelectContent>
        </Select>
        <div className="flex gap-2">
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
          >
            Cancel
          </Button>
          <Button
            type="button"
            disabled={saving || chosen.length === 0 || emptySecrets.length > 0}
            onClick={() => void run()}
          >
            {saving && <Spinner />}
            Set {chosen.length} {chosen.length === 1 ? "variable" : "variables"}
          </Button>
        </div>
      </DialogFooter>
    </div>
  )
}

function RowEditor({
  row,
  error,
  onChange,
}: {
  row: ImportRow
  error?: string
  onChange: (patch: Partial<ImportRow>) => void
}) {
  const [shown, setShown] = useState(false)
  const read = whereRead(row.evidence)
  return (
    <tr
      className={cn(
        "border-b align-top last:border-0",
        !row.include && "opacity-55"
      )}
    >
      <td className="py-2 pl-3">
        <Checkbox
          aria-label={`Set ${row.key}`}
          checked={row.include}
          onCheckedChange={(c) => onChange({ include: c })}
        />
      </td>
      <td className="max-w-56 py-2 pr-3">
        <div
          className="truncate font-mono text-[12.5px] font-medium"
          title={row.key}
        >
          {row.key}
        </div>
        {row.providedBy && (
          <div className="text-xs text-muted-foreground">
            Loomplane sets this when it provisions {row.providedBy}.
          </div>
        )}
        {read && (
          <div
            className="truncate text-xs text-muted-foreground"
            title={row.evidence
              ?.map((e) => `${e.path}:${e.line ?? ""}  ${e.detail}`)
              .join("\n")}
          >
            {read}
          </div>
        )}
      </td>
      <td className="py-2 pr-3">
        <div className="flex gap-1">
          <Input
            aria-label={`${row.key} value`}
            className="h-8 font-mono text-[12.5px]"
            type={row.secret && !shown ? "password" : "text"}
            autoComplete="off"
            spellCheck={false}
            value={row.value}
            placeholder={
              row.secret && row.example ? `example: ${row.example}` : "(empty)"
            }
            onChange={(e) => onChange({ value: e.target.value })}
          />
          {row.secret && (
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={shown ? `Hide ${row.key}` : `Show ${row.key}`}
              onClick={() => setShown((s) => !s)}
            >
              {shown ? <EyeOffIcon /> : <EyeIcon />}
            </Button>
          )}
          <GenerateMenu
            keyName={row.key}
            onPick={(value) => onChange({ value, include: true })}
          />
        </div>
        {error && <p className="mt-1 text-xs text-destructive">{error}</p>}
      </td>
      <td className="py-2 pr-3">
        <Switch
          className="mt-2"
          aria-label={`${row.key} is a secret`}
          checked={row.secret}
          onCheckedChange={(c) => onChange({ secret: c })}
        />
      </td>
    </tr>
  )
}

/**
 * GenerateMenu fills a value with a random one: a key, a token or a
 * password, made in the browser. The one the key most likely wants is
 * first.
 */
export function GenerateMenu({
  keyName,
  onPick,
}: {
  keyName: string
  onPick: (value: string) => void
}) {
  const best = generatorFor(keyName)
  const rest = generators.filter((g) => g.id !== best.id)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            type="button"
            variant="outline"
            size="icon-sm"
            aria-label={
              keyName ? `Generate a value for ${keyName}` : "Generate a value"
            }
            title="Generate a value"
          />
        }
      >
        <DicesIcon />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-64">
        <DropdownMenuGroup>
          <DropdownMenuLabel>Generate</DropdownMenuLabel>
          {[best, ...rest].map((g) => (
            <DropdownMenuItem key={g.id} onClick={() => onPick(g.generate())}>
              <div className="grid">
                <span>{g.label}</span>
                <span className="font-mono text-[11px] text-muted-foreground">
                  {g.hint}
                </span>
              </div>
            </DropdownMenuItem>
          ))}
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
