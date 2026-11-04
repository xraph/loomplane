// Which edition this bundle is. The public dashboard is the default. Loomplane
// Cloud sets VITE_LOOMPLANE_EDITION=cloud at build time and gains pages that
// must never ship in the public bundle.
//
// Code under src/cloud is reachable only behind a branch Vite removes from a
// public build. Where that matters, compare import.meta.env directly at the
// use site, as isCloud does here, rather than importing isCloud: Rollup does
// not always fold a constant across modules. `pnpm check:oss-bundle` fails a
// public build that still carries the cloud marker.
export const isCloud: boolean =
  import.meta.env.VITE_LOOMPLANE_EDITION === "cloud"
