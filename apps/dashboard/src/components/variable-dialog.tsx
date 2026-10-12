import { type FormEvent, useState } from "react"

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
import { keyValid } from "@/lib/variables"

export interface VariableInput {
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
  submit: (input: VariableInput) => Promise<void>
}

/** VariableDialog sets a variable, or changes one's value. */
export function VariableDialog(props: VariableDialogProps) {
  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className="sm:max-w-lg">
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
  submit,
}: VariableDialogProps) {
  const [key, setKey] = useState(editing?.key ?? presetKey ?? "")
  const [value, setValue] = useState(
    editing && !editing.secret ? editing.value : ""
  )
  const [secret, setSecret] = useState(editing?.secret ?? false)
  const [environment, setEnvironment] = useState(editing?.environment ?? "")
  const [error, setError] = useState("")
  const [saving, setSaving] = useState(false)
  const trimmedKey = key.trim()
  const badKey = trimmedKey !== "" && !keyValid(trimmedKey)

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setError("")
    setSaving(true)
    try {
      await submit({
        key: trimmedKey,
        value,
        secret,
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
        <Field data-invalid={badKey || undefined}>
          <FieldLabel htmlFor="variable-key">Key</FieldLabel>
          <Input
            id="variable-key"
            className="font-mono"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            placeholder="DATABASE_URL"
            disabled={!!editing}
            aria-invalid={badKey || undefined}
            autoFocus={!editing && !presetKey}
            required
          />
          {badKey && (
            <FieldDescription>
              Letters, digits and underscores, not starting with a digit.
            </FieldDescription>
          )}
        </Field>
        <Field>
          <FieldLabel htmlFor="variable-value">Value</FieldLabel>
          <Textarea
            id="variable-value"
            className="min-h-20 font-mono"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder={
              editing?.secret
                ? "Enter a new value; the old one can't be shown"
                : ""
            }
            autoFocus={!!editing || !!presetKey}
            required={!!editing?.secret}
          />
        </Field>
        <Field orientation="horizontal">
          <Checkbox
            id="variable-secret"
            checked={secret}
            onCheckedChange={(checked) => setSecret(checked)}
          />
          <FieldLabel htmlFor="variable-secret" className="font-normal">
            Secret: encrypt it, and never show it again
          </FieldLabel>
        </Field>
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
        <Button type="submit" disabled={saving || !trimmedKey || badKey}>
          {saving && <Spinner />}
          {editing ? "Save" : "Set variable"}
        </Button>
      </DialogFooter>
    </form>
  )
}
