import { useEffect, useRef, useState } from "react"

import { useNavigate } from "@/lib/router"

/**
 * useUrlTab keeps a page's tab in the URL as ?tab=, so a tab can be linked
 * and Back works. Local state answers at once and the URL follows; a link
 * that changes ?tab= moves the page too. The fallback tab leaves ?tab= off.
 */
export function useUrlTab<T extends string>(
  current: T | undefined,
  fallback: T
): [T, (next: T) => void] {
  const navigate = useNavigate()
  const [tab, setTab] = useState<T>(current ?? fallback)
  const first = useRef(true)
  useEffect(() => {
    if (first.current) {
      first.current = false
      return
    }
    setTab(current ?? fallback)
  }, [current, fallback])
  const go = (next: T) => {
    setTab(next)
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({
        ...prev,
        tab: next === fallback ? undefined : next,
      }),
      replace: true,
    } as never)
  }
  return [tab, go]
}
