# README artwork

| Asset | Source | Use |
| --- | --- | --- |
| Logo | The dashboard's own files, [loomplane-logo.svg](../../apps/dashboard/src/assets/brand/loomplane-logo.svg) and [loomplane-logo-dark.svg](../../apps/dashboard/src/assets/brand/loomplane-logo-dark.svg). There's no copy here. | README masthead. GitHub picks the dark file when you're in dark mode. |
| [loomplane-cover.png](loomplane-cover.png) | A screenshot of the landing page hero's Overview replica (`apps/docs/src/components/landing/dashboard-shot.tsx`), with its sample data. | README cover. |
| [loomplane-architecture.svg](loomplane-architecture.svg) | Editable SVG drawn from [the architecture guide](../architecture.md) and the application's composition. | Current application architecture. Planned cloud deployment flows are described in the documentation. |

Everything follows the dashboard's ocean accent: blue `#2F5C78` on light backgrounds and `#5AAEE5` on dark ones, with ink `#171717` and paper `#FAFAFA`. Those are the `--brand` token in `apps/dashboard/src/index.css` and the landing's `--blue`, converted to hex. If the brand moves again, the logo follows on its own, because the README points at the dashboard's files. The cover and the diagram you'll have to redo.

## Retaking the cover

Run the docs site (`next dev` in `apps/docs`), open it at 1440 pixels wide with a device scale factor of 2, and screenshot `div.shot` with 56 pixels of the hero around its top and sides. The replica plays a loop, so wait for the frame where a plan for `mono` to production is waiting on the Approve button. Hide the Next.js dev indicator (`nextjs-portal`) first, then scale the result to 1600 pixels wide.

`next dev` also writes an `AGENTS.md` into `apps/docs`. Delete it afterwards; it isn't meant to be committed here.

The README uses local paths, so the images move with the repository and don't depend on a hosted image service.
