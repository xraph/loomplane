// The keyboard shortcut sheet. Each app passes the shortcuts it handles.
import { Fragment } from "react"

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Kbd, KbdGroup } from "@/components/ui/kbd"

/** A group of shortcuts: each is the keys pressed and what they do. */
export type ShortcutGroup = { title: string; keys: [string[], string][] }

export function ShortcutsDialog({
  groups,
  open,
  onOpenChange,
}: {
  groups: ShortcutGroup[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>
            Two-key shortcuts like G D are pressed one after the other.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-5">
          {groups.map((g) => (
            <section key={g.title} className="grid gap-2">
              <h3 className="text-xs font-medium text-muted-foreground">
                {g.title}
              </h3>
              <dl className="grid grid-cols-[1fr_auto] gap-x-4 gap-y-2 text-sm">
                {g.keys.map(([keys, what]) => (
                  <Fragment key={what}>
                    <dt>{what}</dt>
                    <dd>
                      <KbdGroup>
                        {keys.map((k) => (
                          <Kbd key={k}>{k}</Kbd>
                        ))}
                      </KbdGroup>
                    </dd>
                  </Fragment>
                ))}
              </dl>
            </section>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  )
}
