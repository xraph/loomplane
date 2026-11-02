// Steps and editor panes for the How it works section.
export const steps = [
  {
    pane: "cli",
    n: "01",
    title: "Analyze",
    body: "Reads Go, Rust, Node, Python and JVM code, Dockerfiles and Compose files. Every finding links to the line it came from.",
  },
  {
    pane: "manifest",
    n: "02",
    title: "Draft loomplane.yaml",
    body: "Services, resources and gateways, with overrides per environment. Each save is a new revision.",
  },
  {
    pane: "cli",
    n: "03",
    title: "Connect clouds",
    body: "Each key is checked with its cloud before it's kept, and you're told which permissions are missing.",
  },
  {
    pane: "cli",
    n: "04",
    title: "Plan",
    body: "Every action, in order, with anything in the way. A plan whose world changed comes back stale.",
  },
  {
    pane: "cli",
    n: "05",
    title: "Approve and deploy",
    body: "Builds run on each cloud's own builder, in your accounts. A failed step retries from where it failed.",
  },
  {
    pane: "drift",
    n: "06",
    title: "Keep checking",
    body: "A scheduled check compares what's deployed with what's live, and the next plan puts it back.",
  },
] as const;

export const panes: Record<string, string> = {
  manifest:
    '<span class="k">version</span>: <span class="v">1</span>\n\n<span class="k">defaults</span>:\n  <span class="k">provider</span>: <span class="s">cloud-run</span>\n  <span class="k">region</span>: <span class="s">us-central1</span>\n\n<span class="k">services</span>:\n  <span class="k">api</span>:\n    <span class="k">source</span>: <span class="s">go:services/api</span>\n    <span class="k">needs</span>: [<span class="s">db</span>, <span class="s">cache</span>]\n    <span class="k">scale</span>: { <span class="k">min</span>: <span class="v">1</span>, <span class="k">max</span>: <span class="v">5</span> }\n    <span class="k">size</span>: { <span class="k">cpu</span>: <span class="v">1</span>, <span class="k">memory</span>: <span class="v">512</span> }\n  <span class="k">worker</span>:\n    <span class="k">source</span>: <span class="s">services/worker/cmd/worker</span>\n    <span class="k">provider</span>: <span class="s">ecs-fargate</span>\n    <span class="k">needs</span>: [<span class="s">api</span>]\n  <span class="k">web</span>:\n    <span class="k">source</span>: <span class="s">apps/web</span>\n    <span class="k">provider</span>: <span class="s">vercel</span>\n    <span class="k">environments</span>:\n      <span class="k">preview</span>: { <span class="k">enabled</span>: <span class="v">false</span> }\n\n<span class="k">resources</span>:\n  <span class="k">db</span>:    { <span class="k">kind</span>: <span class="s">postgres</span>, <span class="k">provider</span>: <span class="s">cloud-sql</span>, <span class="k">version</span>: <span class="s">"17"</span> }\n  <span class="k">cache</span>: { <span class="k">kind</span>: <span class="s">redis</span>, <span class="k">provider</span>: <span class="s">memorystore</span> }\n\n<span class="k">gateways</span>:\n  <span class="k">edge</span>:\n    <span class="k">routes</span>:\n      - { <span class="k">path</span>: <span class="s">/api</span>, <span class="k">to</span>: <span class="s">api</span>, <span class="k">strip_prefix</span>: <span class="v">true</span> }\n      - { <span class="k">path</span>: <span class="s">/</span>, <span class="k">to</span>: <span class="s">web</span> }',
  cli: '<span class="c"># The same flow from a terminal or CI</span>\n<span class="k">$</span> loomctl login --url https://loom.acme.dev --key -\n<span class="k">$</span> loomctl analyze mono\n<span class="c">  found 3 services, 2 resources, 1 shared package</span>\n\n<span class="k">$</span> loomctl manifest save mono -f loomplane.yaml\n<span class="c">  saved revision 14</span>\n\n<span class="k">$</span> loomctl plan mono --env production\n<span class="c">  plan 7f3a2c · 7 actions · 0 blocking</span>\n<span class="s">  +</span> provision db       cloud-sql    postgres 17\n<span class="s">  +</span> provision cache    memorystore  redis\n<span class="s">  +</span> build     api      cloud-build\n<span class="s">  +</span> deploy    api      cloud-run    us-central1\n<span class="s">  +</span> deploy    worker   ecs-fargate\n<span class="s">  +</span> deploy    web      vercel\n<span class="s">  +</span> route     edge     /api, /\n\n<span class="k">$</span> loomctl approve 7f3a2c\n<span class="s">  approved · deploying revision 14</span>',
  drift:
    '<span class="c"># Every 30 minutes (LOOMPLANE_DRIFT_INTERVAL)</span>\n<span class="v">drift check</span> · production · <span class="c">14:30 UTC</span>\n\n  <span class="s">=</span> db        cloud-sql      matches\n  <span class="s">=</span> cache     memorystore    matches\n  <span class="w">~</span> api       cloud-run      max instances 5 to 12\n                             <span class="c">changed in the console</span>\n  <span class="s">=</span> worker    ecs-fargate    matches\n  <span class="s">=</span> web       vercel         matches\n\n<span class="v">next plan</span>\n  <span class="w">~</span> update    api            max instances 12 to 5\n\n<span class="c"># Review it like any other plan. Approve it, and</span>\n<span class="c"># what runs is what you deployed again.</span>',
};
