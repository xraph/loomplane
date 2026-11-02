import "@testing-library/jest-dom/vitest"
import { cleanup, configure } from "@testing-library/react"
import { afterEach } from "vitest"

// Split routes and query updates can wait behind concurrent build work.
configure({ asyncUtilTimeout: 3_000 })

afterEach(() => {
  cleanup()
  localStorage.clear()
  window.history.replaceState(null, "", "/")
})

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
