// Tests for shim.js, the script the catalog admin proxy puts in each page.
// Run with: node --test internal/catalogproxy/shim_test.mjs
import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import vm from "node:vm"

const source = readFileSync(new URL("./shim.js", import.meta.url), "utf8")
const base = "/catalog-admin/" + "T".repeat(43)

// fakeWindow is the little of a browser window the shim patches. Each
// patched call records the URL it was finally given.
function fakeWindow({ storageThrows = false } = {}) {
  const calls = []
  class XMLHttpRequest {
    open(method, url) {
      calls.push(["xhr", url])
    }
  }
  class WebSocket {
    constructor(url, protocols) {
      calls.push(["ws", url])
      this.protocols = protocols
    }
  }
  WebSocket.OPEN = 1
  class EventSource {
    constructor(url) {
      calls.push(["sse", url])
    }
  }
  class Request {
    constructor(url, init) {
      this.url = url
      this.method = (init && init.method) || "GET"
    }
  }
  const window = {
    location: { protocol: "https:", host: "loomplane.test", href: "https://loomplane.test" + base + "/admin" },
    fetch(input) {
      calls.push(["fetch", typeof input === "string" ? input : input.url])
      return Promise.resolve("response")
    },
    XMLHttpRequest,
    WebSocket,
    EventSource,
    Request,
    history: {
      pushState(state, title, url) {
        calls.push(["push", url])
      },
      replaceState(state, title, url) {
        calls.push(["replace", url])
      },
    },
  }
  for (const name of ["localStorage", "sessionStorage"]) {
    Object.defineProperty(window, name, {
      configurable: true,
      get() {
        if (storageThrows) throw new Error("SecurityError: the document is sandboxed")
        return { getItem: () => "real", setItem() {}, removeItem() {}, clear() {}, key: () => null, length: 0 }
      },
    })
  }
  return { window, calls }
}

function install(window, prefixes = ["/admin", "/fabriq/"]) {
  const shim = vm.runInThisContext(source)
  shim(window, { base, prefixes })
}

function exercise(window, url) {
  window.fetch(url)
  new window.XMLHttpRequest().open("GET", url, true)
  new window.WebSocket(url)
  new window.EventSource(url)
  window.history.pushState({}, "", url)
  window.history.replaceState({}, "", url)
}

test("prefixes fetch, XHR, WebSocket, EventSource and history URLs under each allowed prefix", () => {
  for (const [url, want] of [
    ["/admin", base + "/admin"],
    ["/admin/api/stats?x=1", base + "/admin/api/stats?x=1"],
    ["/fabriq/admin/meta", base + "/fabriq/admin/meta"],
    ["/fabriq", base + "/fabriq"],
    ["https://loomplane.test/admin/routes", "https://loomplane.test" + base + "/admin/routes"],
    ["wss://loomplane.test/admin/ws", "wss://loomplane.test" + base + "/admin/ws"],
  ]) {
    const { window, calls } = fakeWindow()
    install(window)
    exercise(window, url)
    assert.deepEqual(
      calls,
      ["fetch", "xhr", "ws", "sse", "push", "replace"].map((kind) => [kind, want]),
      url,
    )
  }
  // A URL object and a Request are rewritten too, and the WebSocket keeps
  // its constants and protocols.
  const { window, calls } = fakeWindow()
  install(window)
  window.fetch(new URL("https://loomplane.test/admin/x"))
  window.fetch(new window.Request("https://loomplane.test/admin/y", { method: "POST" }))
  const socket = new window.WebSocket("/admin/ws", ["v1"])
  assert.deepEqual(calls, [
    ["fetch", "https://loomplane.test" + base + "/admin/x"],
    ["fetch", "https://loomplane.test" + base + "/admin/y"],
    ["ws", base + "/admin/ws"],
  ])
  assert.equal(window.WebSocket.OPEN, 1)
  assert.ok(socket instanceof window.WebSocket)
  assert.deepEqual(socket.protocols, ["v1"])
})

test("leaves other URLs alone", () => {
  for (const url of [
    "/static/x",
    "https://cdn.example/x",
    "https://cdn.example/admin/x",
    "//cdn.example/admin/x",
    "/administrator",
    "/fabriqx/y",
    "relative/admin",
    base + "/admin/already",
  ]) {
    const { window, calls } = fakeWindow()
    install(window)
    exercise(window, url)
    assert.deepEqual(
      calls,
      ["fetch", "xhr", "ws", "sse", "push", "replace"].map((kind) => [kind, url]),
      url,
    )
  }
  // history calls without a URL pass through.
  const { window, calls } = fakeWindow()
  install(window)
  window.history.pushState({}, "")
  assert.deepEqual(calls, [["push", undefined]])
})

test("falls back to in-memory storage when localStorage throws", () => {
  const { window } = fakeWindow({ storageThrows: true })
  install(window)
  for (const name of ["localStorage", "sessionStorage"]) {
    const storage = window[name]
    assert.equal(storage.getItem("theme"), null)
    storage.setItem("theme", 1)
    assert.equal(storage.getItem("theme"), "1")
    assert.equal(storage.length, 1)
    assert.equal(storage.key(0), "theme")
    storage.removeItem("theme")
    assert.equal(storage.length, 0)
    storage.setItem("a", "b")
    storage.clear()
    assert.equal(storage.getItem("a"), null)
  }
  // Storage that works is left alone.
  const working = fakeWindow()
  install(working.window)
  assert.equal(working.window.localStorage.getItem("anything"), "real")
})

test("installs once", () => {
  const { window, calls } = fakeWindow()
  install(window)
  install(window)
  window.fetch("/admin/x")
  assert.deepEqual(calls, [["fetch", base + "/admin/x"]])
})
