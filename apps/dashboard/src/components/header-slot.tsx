// Where the top bar goes when the page scrolls in a column of its own (the
// agent docked beside it): a slot above that column, so the column's
// scrollbar starts under the bar instead of beside it. With no slot, the
// bar renders where the page put it, as it always has.
import { createContext, type ReactNode, useContext, useState } from "react"
import { createPortal } from "react-dom"

const Slot = createContext<HTMLElement | null>(null)

export const HeaderSlotProvider = Slot.Provider

/** InHeaderSlot renders its children in the header slot when there is
 * one, and in place otherwise. */
export function InHeaderSlot({ children }: { children: ReactNode }) {
  const slot = useContext(Slot)
  return slot ? createPortal(children, slot) : children
}

/**
 * HeaderSlotAbove gives its children a slot above them, for a layout that
 * wraps its pages in padding: the pages' top bar goes to the top of the
 * page, full width, not inside the layout's column. A slot from further
 * out, such as the docked agent's, wins.
 */
export function HeaderSlotAbove({ children }: { children: ReactNode }) {
  const outer = useContext(Slot)
  const [own, setOwn] = useState<HTMLElement | null>(null)
  // The same tree either way, so docking the agent doesn't remount the page.
  return (
    <>
      {/* contents keeps the sticky bar's parent the page's scrolling column. */}
      <div ref={setOwn} className="contents" />
      <Slot.Provider value={outer ?? own}>{children}</Slot.Provider>
    </>
  )
}
