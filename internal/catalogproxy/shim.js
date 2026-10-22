// Loomplane's catalog admin shim. The proxy puts it at the top of each
// admin page it serves, called with the page's window and
// {base: "/catalog-admin/<token>", prefixes: [...]}. It keeps the page's
// own requests and history entries under the session's prefix, and gives
// the sandboxed page in-memory storage where the real one throws.
(function (window, config) {
  "use strict"
  if (window.__loomplaneCatalogAdmin) return
  window.__loomplaneCatalogAdmin = true

  var base = config.base
  var prefixes = config.prefixes.map(function (p) {
    return p.replace(/\/+$/, "")
  })
  var boundary = /^(?:$|[/?#])/
  var absolute = /^([a-z][a-z0-9+.-]*:)\/\/([^/?#]*)(.*)$/i

  function underPrefix(path) {
    if (path === base || path.indexOf(base + "/") === 0) return false
    for (var i = 0; i < prefixes.length; i++) {
      var p = prefixes[i]
      if (path.indexOf(p) === 0 && boundary.test(path.slice(p.length))) return true
    }
    return false
  }

  // rewrite moves an admin path, or an absolute URL to it on this host,
  // under the session. Anything else is left alone.
  function rewrite(url) {
    if (typeof url !== "string") return url
    if (url.charAt(0) === "/") {
      if (url.charAt(1) === "/") return url
      return underPrefix(url) ? base + url : url
    }
    var m = absolute.exec(url)
    if (!m || !/^(https?|wss?):$/i.test(m[1])) return url
    var host = window.location && window.location.host
    if (!host || m[2].toLowerCase() !== String(host).toLowerCase()) return url
    var rest = m[3]
    if (rest.charAt(0) !== "/" || !underPrefix(rest)) return url
    return m[1] + "//" + m[2] + base + rest
  }

  function isURL(value) {
    return value != null && typeof value === "object" && typeof value.href === "string" && typeof value.pathname === "string"
  }

  var fetch = window.fetch
  if (typeof fetch === "function") {
    window.fetch = function (input, init) {
      if (typeof input === "string") {
        input = rewrite(input)
      } else if (isURL(input)) {
        input = rewrite(input.href)
      } else if (input != null && typeof input.url === "string" && typeof window.Request === "function") {
        var moved = rewrite(input.url)
        if (moved !== input.url) input = new window.Request(moved, input)
      }
      return arguments.length > 1 ? fetch.call(window, input, init) : fetch.call(window, input)
    }
  }

  var XHR = window.XMLHttpRequest
  if (XHR && XHR.prototype && typeof XHR.prototype.open === "function") {
    var open = XHR.prototype.open
    XHR.prototype.open = function (method, url) {
      var args = Array.prototype.slice.call(arguments)
      if (args.length > 1 && args[1] != null) args[1] = rewrite(String(args[1]))
      return open.apply(this, args)
    }
  }

  function wrapConstructor(name) {
    var Original = window[name]
    if (typeof Original !== "function") return
    var Wrapped = function (url, options) {
      var moved = url == null ? url : rewrite(isURL(url) ? url.href : String(url))
      return arguments.length > 1 ? new Original(moved, options) : new Original(moved)
    }
    Wrapped.prototype = Original.prototype
    ;["CONNECTING", "OPEN", "CLOSING", "CLOSED"].forEach(function (k) {
      if (k in Original) Wrapped[k] = Original[k]
    })
    window[name] = Wrapped
  }
  wrapConstructor("WebSocket")
  wrapConstructor("EventSource")

  var history = window.history
  if (history) {
    ;["pushState", "replaceState"].forEach(function (name) {
      var original = history[name]
      if (typeof original !== "function") return
      history[name] = function (state, title, url) {
        if (arguments.length > 2 && url != null) {
          return original.call(history, state, title, rewrite(isURL(url) ? url.href : String(url)))
        }
        return original.apply(history, arguments)
      }
    })
  }

  // A sandboxed page has an opaque origin, so its storage throws. An
  // admin that keeps a theme or a draft there gets one that lasts as long
  // as the page.
  function memoryStorage() {
    var data = Object.create(null)
    var has = function (k) {
      return Object.prototype.hasOwnProperty.call(data, k)
    }
    return {
      get length() {
        return Object.keys(data).length
      },
      key: function (i) {
        var keys = Object.keys(data)
        return i >= 0 && i < keys.length ? keys[i] : null
      },
      getItem: function (k) {
        k = String(k)
        return has(k) ? data[k] : null
      },
      setItem: function (k, v) {
        data[String(k)] = String(v)
      },
      removeItem: function (k) {
        delete data[String(k)]
      },
      clear: function () {
        data = Object.create(null)
      },
    }
  }
  ;["localStorage", "sessionStorage"].forEach(function (name) {
    try {
      var storage = window[name]
      if (storage && typeof storage.getItem === "function") {
        storage.getItem("loomplane")
        return
      }
    } catch (e) {
      // Falls through to the in-memory store.
    }
    try {
      Object.defineProperty(window, name, { configurable: true, enumerable: true, value: memoryStorage() })
    } catch (e) {
      // The page keeps the storage it has.
    }
  })
})
