import "@testing-library/jest-dom/vitest"
import { cleanup, configure } from "@testing-library/react"
import { afterEach } from "vitest"

import { FakeEventSource } from "@/test-fixtures/event-source"

// Split routes and query updates can wait behind concurrent build work.
configure({ asyncUtilTimeout: 3_000 })

afterEach(() => {
  cleanup()
  localStorage.clear()
  window.history.replaceState(null, "", "/")
  FakeEventSource.reset()
})

// jsdom has no EventSource; the agent's task stream opens one.
if (!("EventSource" in window)) {
  Object.defineProperty(window, "EventSource", {
    configurable: true,
    writable: true,
    value: FakeEventSource,
  })
}

// Nor ResizeObserver or element scrolling, which the conversation log uses
// to stay at the bottom.
if (!("ResizeObserver" in window)) {
  Object.defineProperty(window, "ResizeObserver", {
    configurable: true,
    writable: true,
    value: class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  })
}
if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {}
if (!Element.prototype.scrollIntoView)
  Element.prototype.scrollIntoView = () => {}

// jsdom has no matchMedia; the theme provider and sidebar ask for it.
if (!window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList
}
