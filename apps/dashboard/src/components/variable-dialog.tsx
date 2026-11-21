import { type FormEvent, type ReactNode, useState } from "react"

import { messageOf } from "@/client/errors"
import type { Variable } from "@/client/forge/loomplane/types"
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
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { GenerateMenu } from "@/components/variable-import"
import { keyValid } from "@/lib/variables"

export type ResourceOption = { value: string; label: string }

export interface VariableInput {
  resourceOutput?: string
  key: string
  value: string
  secret: boolean
  environment: string
}

/**
 * Environments says how a variable picks its environment. A project's
 * chooses between every environment and the one being looked at; an
 * organization-wide one names any environment, which then applies in
 * every project that has it.
 */
export type Environments =
  { mode: "project"; current: string } | { mode: "organization" }

export interface VariableDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  environments: Environments
  /** The variable being edited; create a new one when absent. */
  editing?: Variable
  /** A key to start from, when overriding an inherited one. */
  presetKey?: string
  resourceOptions?: ResourceOption[]
  mappingOnly?: boolean
  scopePicker?: ReactNode
  defaultEnvironment?: string
  submit: (input: VariableInput) => Promise<void>
}

/** VariableDialog sets a variable, or changes one's value. */
export function VariableDialog(props: VariableDialogProps) {
  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-[500px]">
        {/* Mounted fresh each time it opens, so it starts from its props. */}
        {props.open && <VariableForm {...props} />}
      </DialogContent>
    </Dialog>
  )
}

function VariableForm({
  onOpenChange,
  title,
  description,
  environments,
  editing,
  presetKey,
  resourceOptions = [],
  mappingOnly,
  scopePicker,
  defaultEnvironment,
  submit,
}: VariableDialogProps) {
  const [mapping, setMapping] = useState(
    !!(mappingOnly || editing?.resourceOutput)
  )
  const [reference, setReference] = useState(editing?.resourceOutput ?? "")
  const [manual, setManual] = useState(
    !!editing?.resourceOutput &&
      !resourceOptions.some((r) => r.value === editing.resourceOutput)
  )
  const [key, setKey] = useState(editing?.key ?? presetKey ?? "")
  const [value, setValue] = useState(
    editing && !editing.secret ? editing.value : ""
  )
  const [secret, setSecret] = useState(editing?.secret ?? false)
  const [environment, setEnvironment] = useState(
    editing?.environment ?? defaultEnvironment ?? ""
  )
  const [error, setError] = useState("")
  const [saving, setSaving] = useState(false)
  const trimmedKey = key.trim()
  const selectedReference =
    mappingOnly && !resourceOptions.some((option) => option.value === reference)
      ? ""
      : reference.trim()
  const badKey = trimmedKey !== "" && !keyValid(trimmedKey)

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    setSaving(true)
    try {
      await submit({
        key: trimmedKey,
        value: mapping ? "" : value,
        secret: mapping ? false : secret,
        resourceOutput: mapping ? selectedReference : undefined,
        environment: environment.trim(),
      })
      onOpenChange(false)
    } catch (err) {
      setError(messageOf(err, "The variable wasn't saved."))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form onSubmit={(e) => void onSubmit(e)}>
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>{description}</DialogDescription>
      </DialogHeader>
      <FieldGroup className="py-4">
        {scopePicker}
        {!mappingOnly && (
          <Field>
            <FieldLabel>Value source</FieldLabel>
            <Select
              value={mapping ? "resource" : "value"}
              items={[
                { value: "value", label: "Value" },
                { value: "resource", label: "Resource output" },
              ]}
              onValueChange={(source) => setMapping(source === "resource")}
            >
              <SelectTrigger aria-label="Value source" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="value">Value</SelectItem>
                <SelectItem value="resource">Resource output</SelectItem>
              </SelectContent>
            </Select>
          </Field>
        )}
        <Field data-invalid={badKey || undefined}>
          <FieldLabel htmlFor="variable-key">Key</FieldLabel>
          <Input
            id="variable-key"
            className="font-mono text-[13px]"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            placeholder="DATABASE_URL"
            disabled={!!editing}
            aria-invalid={badKey || undefined}
            autoFocus={!editing && !presetKey}
            required
          />
          {badKey ? (
            <FieldError>
              That key won&apos;t work. Use letters, digits and underscores, not
              starting with a digit.
            </FieldError>
          ) : (
            !editing && (
              <FieldDescription>
                Letters, digits and underscores, not starting with a digit.
              </FieldDescription>
            )
          )}
        </Field>
        {mapping ? (
          <Field>
            <FieldLabel htmlFor="variable-resource">Resource output</FieldLabel>
            {resourceOptions.length > 0 && (
              <Select
                items={[
                  ...resourceOptions,
                  ...(mappingOnly
                    ? []
                    : [{ value: ":manual", label: "Enter a resource name…" }]),
                ]}
                value={manual ? ":manual" : selectedReference || null}
                onValueChange={(ref) => {
                  setManual(ref === ":manual")
                  setReference(ref === ":manual" ? "" : String(ref))
                }}
              >
                <SelectTrigger
                  id={manual ? undefined : "variable-resource"}
                  aria-label="Choose a resource output"
                  className="w-full"
                >
                  <SelectValue placeholder="Choose a resource output" />
                </SelectTrigger>
                <SelectContent>
                  {resourceOptions.map((r) => (
                    <SelectItem key={r.value} value={r.value}>
                      {r.label}
                    </SelectItem>
                  ))}
                  {!mappingOnly && (
                    <SelectItem value=":manual">
                      Enter a resource name…
                    </SelectItem>
                  )}
                </SelectContent>
              </Select>
            )}
            {!mappingOnly && (manual || !resourceOptions.length) && (
              <Input
                id="variable-resource"
                placeholder="postgres or fabriq.url"
                value={reference}
                onChange={(e) => setReference(e.target.value)}
                required
              />
            )}
            <FieldDescription>
              Services that use this resource receive its output as a secret at
              deployment.
            </FieldDescription>
          </Field>
        ) : (
          <>
            <Field>
              <div className="flex items-center justify-between">
                <FieldLabel htmlFor="variable-value">Value</FieldLabel>
                <GenerateMenu keyName={trimmedKey} onPick={setValue} />
              </div>
              <Textarea
                id="variable-value"
                className="min-h-16 font-mono text-[12.5px]"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                placeholder={
                  editing?.secret
                    ? "Enter a new value; the old one can't be shown"
                    : "The value"
                }
                autoFocus={!!editing || !!presetKey}
                required={!!editing?.secret}
              />
            </Field>
            <Field
              orientation="horizontal"
              className="items-start rounded-lg bg-subtle px-3 py-2.5"
            >
              <Checkbox
                id="variable-secret"
                className="mt-0.5"
                checked={secret}
                onCheckedChange={(checked) => setSecret(checked)}
              />
              <FieldLabel
                htmlFor="variable-secret"
                className="grid gap-0.5 text-[13px] font-medium"
              >
                Secret
                <span className="font-normal text-muted-foreground">
                  Encrypt it, and never show it again.
                </span>
              </FieldLabel>
            </Field>
          </>
        )}
        {!editing && environments.mode === "project" && (
          <Field>
            <FieldLabel>Applies in</FieldLabel>
            <Select
              items={[
                { label: "Every environment", value: "" },
                {
                  label: `${environments.current} only`,
                  value: environments.current,
                },
              ]}
              value={environment}
              onValueChange={(v) => setEnvironment(v as string)}
            >
              <SelectTrigger aria-label="Applies in" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="">Every environment</SelectItem>
                <SelectItem value={environments.current}>
                  {environments.current} only
                </SelectItem>
              </SelectContent>
            </Select>
          </Field>
        )}
        {!editing && environments.mode === "organization" && (
          <Field>
            <FieldLabel htmlFor="variable-environment">Environment</FieldLabel>
            <Input
              id="variable-environment"
              value={environment}
              onChange={(e) => setEnvironment(e.target.value.toLowerCase())}
              placeholder="Every environment"
            />
            <FieldDescription>
              Leave it empty for every environment, or name one, like
              production: it then applies in every project&apos;s environment of
              that name.
            </FieldDescription>
          </Field>
        )}
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
      </FieldGroup>
      <DialogFooter>
        <Button
          type="button"
          variant="outline"
          onClick={() => onOpenChange(false)}
        >
          Cancel
        </Button>
        <Button
          type="submit"
          disabled={
            saving || !trimmedKey || badKey || (mapping && !selectedReference)
          }
        >
          {saving && <Spinner />}
          {editing
            ? "Save"
            : mappingOnly
              ? "Save mapping and plan again"
              : "Set variable"}
        </Button>
      </DialogFooter>
    </form>
  )
}
