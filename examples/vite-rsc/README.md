# Vite RSC on Loomplane

This app builds React Server Components with `@vitejs/plugin-rsc` and serves them from Node. You can open a nested path, receive streamed server content, and call a server function from the browser.

```sh
npm ci
npm run build
NODE_ENV=production npm start
```

Open `http://localhost:3000/projects/demo`. Click **Call server function** to check browser hydration and the action response. `/health` returns `ok`.

The Dockerfile builds and retains the whole application, including the client, RSC and SSR outputs and their dependencies. It runs as the Node image's unprivileged user, binds to `0.0.0.0`, and reads `PORT`. Keep `NODE_ENV=production` when starting a production build so the server and browser use the same React mode. Behind a TLS-terminating proxy, set `PUBLIC_ORIGIN` to your public HTTPS origin, such as `https://rsc.example.com`, so the action origin check uses the public address.

## Deploy

Add this directory as your repository source root. Loomplane detects `vite-rsc` as a web app and attaches the Dockerfile. The included `loomplane.yaml` selects Render. You can choose another container target and supply its connection and build settings through your project.

For a remote SSH or Kubernetes build, keep the Dockerfile and select the required builder/registry or cluster build setup. Loomplane's existing image releases pin the complete container, so promotion and rollback retain matching client/server manifests.

You can also deploy a Dockerfile-free Node app through the existing native build path when you provide production `build` and `start` scripts. This example uses npm. Other package managers and monorepos can define their install/build steps in a Dockerfile.

Vercel's generic Vite preset does not deploy this app's Node server. Dynamic Vite RSC deployments to Vercel are blocked until Loomplane has a suitable adapter. A fully generated static site can explicitly set `kind: static-site`; confirm its publish directory matches your target's static output convention.

The example has one stateless demonstration action and no user data. It checks the request origin before dispatching it. Your app must enforce authentication and authorization for its own server functions.

## Verification scope

Local checks cover source detection, plan classification and runtime requirements, Docker context preparation, the production container, deep links, RSC streaming, hydration, and a server function. Cloud deployment and cloud rollback require a configured target and are not established by these checks.
