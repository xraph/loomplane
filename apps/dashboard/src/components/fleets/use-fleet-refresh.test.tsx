import { act, renderHook } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { useFleetRefresh } from "./use-fleet-refresh"
afterEach(() => vi.useRealTimers())
it("refreshes without waiting for unrelated global streams and prevents overlapping reads", async () => {
  vi.useFakeTimers()
  let finish: () => void = () => {}
  const refresh = vi.fn(
    () =>
      new Promise<void>((resolve) => {
        finish = resolve
      })
  )
  const hook = renderHook(() => useFleetRefresh(true, refresh))
  await act(async () => {
    vi.advanceTimersByTime(15000)
  })
  expect(refresh).toHaveBeenCalledTimes(1)
  await act(async () => {
    vi.advanceTimersByTime(30000)
  })
  expect(refresh).toHaveBeenCalledTimes(1)
  await act(async () => finish())
  await act(async () => {
    vi.advanceTimersByTime(15000)
  })
  expect(refresh).toHaveBeenCalledTimes(2)
  hook.unmount()
  await act(async () => finish())
  await act(async () => {
    vi.advanceTimersByTime(30000)
  })
  expect(refresh).toHaveBeenCalledTimes(2)
})
it("does not poll a denied or missing scope", async () => {
  vi.useFakeTimers()
  const refresh = vi.fn(async () => {})
  renderHook(() => useFleetRefresh(false, refresh))
  await act(async () => {
    vi.advanceTimersByTime(60000)
  })
  expect(refresh).not.toHaveBeenCalled()
})
