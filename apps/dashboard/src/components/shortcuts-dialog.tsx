// The keyboard shortcut sheet. Each app passes the shortcuts it handles.
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
      <DialogContent className="sm:max-w-[720px]">
        <DialogHeader>
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>
            Two-key shortcuts like G D are pressed one after the other.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-x-6 gap-y-4 sm:grid-cols-2">
          {groups.map((g) => (
            <section key={g.title} className="grid content-start">
              <h3 className="pt-1 pb-1.5 text-xs font-medium text-muted-foreground">
                {g.title}
              </h3>
              <dl className="grid text-[13px]">
                {g.keys.map(([keys, what]) => (
                  <div
                    key={what}
                    className="flex items-center gap-2.5 border-t py-[7px]"
                  >
                    <dt className="flex-1">{what}</dt>
                    <dd>
                      <KbdGroup>
                        {keys.map((k) => (
                          <Kbd key={k}>{k}</Kbd>
                        ))}
                      </KbdGroup>
                    </dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  )
}
