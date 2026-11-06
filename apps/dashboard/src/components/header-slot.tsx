// Where the top bar goes when the page scrolls in a column of its own (the
// agent docked beside it): a slot above that column, so the column's
// scrollbar starts under the bar instead of beside it. With no slot, the
// bar renders where the page put it, as it always has.
import { createContext, type ReactNode, useContext } from "react"
import { createPortal } from "react-dom"

const Slot = createContext<HTMLElement | null>(null)

export const HeaderSlotProvider = Slot.Provider

/** InHeaderSlot renders its children in the header slot when there is
 * one, and in place otherwise. */
export function InHeaderSlot({ children }: { children: ReactNode }) {
  const slot = useContext(Slot)
  return slot ? createPortal(children, slot) : children
}
