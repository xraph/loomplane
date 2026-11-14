// Whether the workspace needs a card before anything runs in it. Only
// Loomplane Cloud ever sets it: the cloud card gate reports it to the layout,
// which provides it here, so shared code (the sidebar, for one) can disable
// what would be refused without importing anything from src/cloud. The
// public edition never provides it and reads false.
import { createContext, useContext } from "react"

export const CardRequiredContext = createContext(false)

/** useCardRequired says whether the workspace is waiting on a card. */
export function useCardRequired(): boolean {
  return useContext(CardRequiredContext)
}
