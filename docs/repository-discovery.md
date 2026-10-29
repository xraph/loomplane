# Repository discovery

Analyze your repository to see its application graph. Loomplane reads manifests and source code without running a build, downloading dependencies, or executing anything from the checkout. Every detected service and backend has source evidence you can inspect.

## Examples and development helpers

The default graph excludes executables under `example`, `examples`, `sample`, `samples`, `fixture`, `fixtures`, `testdata`, `tests`, and `__tests__` directories. It also excludes `scripts`, `tools`, `tooling`, `generators`, `codegen`, and `generated` directories. These checks apply to each path segment across languages, including container images.

You can still use an example as an application. Set the source's root to that directory, or declare the executable as a Forge app or build target. Selecting `examples/polyglot` includes that application; nested test-data directories remain excluded until you select or declare them too.

Loomplane keeps the full repository analysis and applies selection when composing the project graph. A selected app can retain shared libraries outside its root, along with their backend requirements. Excluded services do not contribute their own resources or workspace metadata. The graph's analysis notes explain exclusions.

A Dockerfile whose missing source inputs exist only as `.txt`, `.tmpl`, `.tpl`, or `.template` files is a generator template. It produces a note instead of an application image.

## Forge applications

Loomplane reads `.forge.yaml` and `.forge.yml` project layouts, workspace globs, custom application paths, and `build.apps` targets. App manifests provide names and roles:

```yaml
app:
  name: analysis-worker
  type: worker
```

Use `web`, `server`, or `api` for an HTTP service, `worker` for background work, and `cli` for a command-line tool. A declared worker remains a worker when it exposes health HTTP. CLI entries stay visible for inspection and are not deployable services.

Go discovery follows referenced local functions, callbacks, constructors, and types. Separate app constructors in the same package can therefore have different framework and backend findings. Imports alone remain weaker evidence than an explicit driver option or configuration.

## Forge ecosystem backends

Registered extension constructors and literal driver options, such as `grove.WithDSN("postgres", ...)`, contribute backend evidence to the app that reaches them. Scoped configuration also supports Grove databases and KV stores, cache, queue, Trove stores, events, and search backends.

Configuration follows the app's declared name and project config directory: `config/<app>.yaml` or `.yml`, then app-local `config.yaml` or `.yml`, then shared project defaults for registered extensions. An app-specific embedded backend or disabled extension overrides its shared default. In-memory and local-file backends do not request managed infrastructure.

Driver evidence records the selected backend and source line without copying credentials or connection strings. Malformed configuration and unsupported drivers produce notes. Dynamic factories, interface dispatch, environment-dependent selection, and runtime plugins can require explicit declarations or review; analysis never evaluates them to guess a backend.

## Vite React Server Components

Loomplane recognizes `@vitejs/plugin-rsc` apps as `vite-rsc` web apps when the package has a Vite config or runs Vite in its scripts. You do not need an `index.html`. A dependency without app configuration or scripts stays library evidence.

For dynamic RSC, provide a production `start` script or a Dockerfile. Your server must serve the client assets and the RSC/SSR output from the same build, listen on `0.0.0.0`, and respect the configured port. Use `NODE_ENV=production` for production builds. The plugin's `vite preview` command is for previewing a build; define your production server explicitly.

The existing Node build paths and container targets handle these applications. Remote SSH and Kubernetes source builds require a Dockerfile, as they do for other Node apps. Dynamic deployment to Vercel is blocked until an adapter can package the server correctly. If your app generates all its pages at build time, explicitly select `kind: static-site` and verify the target's publish-directory convention.

See [the deployable Vite RSC example](../examples/vite-rsc/README.md) for a complete Node server and Dockerfile. Detection reads files without executing your Vite config.
