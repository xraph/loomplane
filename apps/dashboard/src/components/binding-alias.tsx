import { PencilIcon } from "lucide-react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import { useBindingsSetAlias } from "@/client/forge/loomplane/hooks"
import type { Binding } from "@/client/forge/loomplane/types"
import { useMutation } from "@/client/hooks"
import { Alert, AlertDescription } from "@/components/ui/alert"
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
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"

/**
 * aliasOf is the name people see for what a binding runs: its alias, or
 * the manifest name when nobody gave it one.
 */
export function aliasOf(binding: Binding): string {
  return binding.alias || binding.name
}

/**
 * BindingAlias names what a binding runs. The alias changes nothing in the
 * cloud, so the dialog starts from Loomplane's suggestion when none is set.
 */
export function BindingAlias({
  projectId,
  environment,
  binding,
}: {
  projectId: string
  environment: string
  binding: Binding
}) {
  const setAlias = useMutation(useBindingsSetAlias)
  const [open, setOpen] = useState(false)
  const [alias, setAliasValue] = useState("")
  const [error, setError] = useState("")
  const suggestion = binding.suggestedAlias ?? ""
  const close = (next: boolean) => {
    if (next) setAliasValue(binding.alias || suggestion)
    setError("")
    setOpen(next)
  }
  const save = async (value: string) => {
    setError("")
    try {
      await setAlias.mutateAsync({
        path: { id: projectId, binding: binding.id },
        body: { alias: value.trim() },
      })
      toast.success(
        value.trim() ? `Named ${binding.name} ${value.trim()}` : "Alias cleared"
      )
      setOpen(false)
    } catch (err) {
      setError(messageOf(err, "The alias wasn't saved."))
    }
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void save(alias)
  }
  return (
    <>
      <Button
        type="button"
        size="icon"
        variant="ghost"
        className="size-6"
        aria-label={`Name ${binding.name} in ${environment}`}
        onClick={() => close(true)}
      >
        <PencilIcon className="size-3.5" />
      </Button>
      <Dialog open={open} onOpenChange={close}>
        <DialogContent className="sm:max-w-md">
          <form onSubmit={submit} className="grid gap-5">
            <DialogHeader>
              <DialogTitle>Name {binding.name}</DialogTitle>
              <DialogDescription>
                What Loomplane shows for {binding.name} in {environment}. Its
                cloud resources keep their names.
              </DialogDescription>
            </DialogHeader>
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="binding-alias">Alias</FieldLabel>
                <Input
                  id="binding-alias"
                  value={alias}
                  onChange={(e) => setAliasValue(e.target.value.toLowerCase())}
                  placeholder={suggestion || binding.name}
                  className="font-mono"
                  maxLength={63}
                  autoFocus
                />
                <FieldDescription>
                  Lower case letters, digits and hyphens, starting with a
                  letter.
                  {suggestion && alias !== suggestion && (
                    <>
                      {" "}
                      <button
                        type="button"
                        className="underline underline-offset-2"
                        onClick={() => setAliasValue(suggestion)}
                      >
                        Use {suggestion}
                      </button>
                    </>
                  )}
                </FieldDescription>
              </Field>
              {error && (
                <Alert variant="destructive">
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              )}
            </FieldGroup>
            <DialogFooter>
              {binding.alias && (
                <Button
                  type="button"
                  variant="ghost"
                  disabled={setAlias.isPending}
                  onClick={() => void save("")}
                >
                  Clear
                </Button>
              )}
              <Button
                type="button"
                variant="outline"
                onClick={() => close(false)}
              >
                Cancel
              </Button>
              <Button
                type="submit"
                disabled={setAlias.isPending || alias.trim() === binding.alias}
              >
                Save
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  )
}
