import type { Metadata } from "next";
import Link from "next/link";
import { Code } from "@/components/landing/code";

export const metadata: Metadata = {
  title: "Platform",
  description:
    "Discovery, manifests, plans, live deployments, drift and gateways.",
};

export default function PlatformPage() {
  return (
    <>
      <section className="page-hero grain" aria-labelledby="platform-title">
        <div className="wrap">
          <div className="crumb rise">
            <Link href="/">Loomplane</Link> / <b>Platform</b>
          </div>
          <h1 id="platform-title">
            See every change <span className="thread">before it happens.</span>
          </h1>
          <p className="lede">
            Discovery tells you what your repositories contain. The manifest
            says what to deploy. A plan shows every action it would take. You
            approve that exact plan, and Loomplane carries it out and keeps it
            true.
          </p>
          <div className="subnav" data-subnav>
            <button type="button" data-jump="p-discovery">
              Discovery
            </button>
            <button type="button" data-jump="p-manifest">
              Manifest
            </button>
            <button type="button" data-jump="p-plans">
              Plans and approvals
            </button>
            <button type="button" data-jump="p-deploy">
              Live deployments
            </button>
            <button type="button" data-jump="p-drift">
              Drift
            </button>
            <button type="button" data-jump="p-gateways">
              Gateways and networking
            </button>
          </div>
        </div>
      </section>

      <div className="wrap">
        <div className="feature" id="p-discovery">
          <div className="txt">
            <div className="eyebrow">Discovery</div>
            <h3 className="h3">
              It reads your monorepo so you don't have to describe it.
            </h3>
            <p>
              Point Loomplane at a GitHub URL. It walks the checkout and finds
              the services it would build, the packages they share and the
              databases they need. Every result carries source evidence you can
              open.
            </p>
            <ul className="ticks">
              <li>
                Go, Rust, Node, Python and JVM (Maven and Gradle) projects,
                Dockerfiles and Compose files
              </li>
              <li>Forge layouts, app roles and scoped backend configuration</li>
              <li>
                Examples, fixtures, test data and scripts stay out of the
                default graph
              </li>
              <li>
                Private repositories through a GitHub App, re-analyzed on every
                push
              </li>
              <li>
                An application split across several repositories, analyzed as
                one project
              </li>
            </ul>
          </div>
          <div className="vis vis-stack">
            <span className="ide-cross vis-cross a" aria-hidden="true"></span>
            <span className="ide-cross vis-cross b" aria-hidden="true"></span>
            <div className="vis-ghost ide-bezel" aria-hidden="true">
              <div className="ide">
                <div className="ghost-h"></div>
                <div className="ghost-lines">
                  <i></i>
                  <i></i>
                  <i></i>
                  <i></i>
                  <i></i>
                </div>
              </div>
            </div>
            <div className="ide-bezel vis-main">
              <div className="panel">
                <div className="panel-h">
                  <b>Analysis · acme/mono</b>
                  <small>commit 3f9e1a · 41s</small>
                </div>
                <div className="rows">
                  <div className="row">
                    <div className="l">
                      <span className="dot blue"></span>
                      <div>
                        <b>api</b>
                        <small>go:services/api · cmd main.go:12</small>
                      </div>
                    </div>
                    <span className="chip blue">http-service</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot blue"></span>
                      <div>
                        <b>worker</b>
                        <small>services/worker/cmd/worker · main.go:9</small>
                      </div>
                    </div>
                    <span className="chip">worker</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot blue"></span>
                      <div>
                        <b>web</b>
                        <small>apps/web · package.json:5 next</small>
                      </div>
                    </div>
                    <span className="chip blue">web-app</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot ok"></span>
                      <div>
                        <b>db</b>
                        <small>postgres · services/api/go.mod:14 pgx</small>
                      </div>
                    </div>
                    <span className="chip ok">resource</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot ok"></span>
                      <div>
                        <b>cache</b>
                        <small>redis · compose.yaml:22</small>
                      </div>
                    </div>
                    <span className="chip ok">resource</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot wait"></span>
                      <div>
                        <b>examples/polyglot</b>
                        <small>excluded · examples directory</small>
                      </div>
                    </div>
                    <span className="chip">skipped</span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>

        <div className="feature flip" id="p-manifest">
          <div className="txt">
            <div className="eyebrow">loomplane.yaml</div>
            <h3 className="h3">A versioned manifest, drafted for you.</h3>
            <p>
              Loomplane drafts <code>loomplane.yaml</code> from the analysis.
              Edit it in the dashboard, in your editor, or with{" "}
              <code>loomctl manifest save</code>. Every save is a new revision,
              and each plan names the revision it came from.
            </p>
            <ul className="ticks">
              <li>
                Services, resources, gateways and per-environment overrides
              </li>
              <li>A JSON schema for completion and checks in your editor</li>
              <li>
                Placement controls that edit the YAML and keep your comments
              </li>
              <li>
                Unknown fields and unsupported versions are rejected, with the
                line
              </li>
            </ul>
          </div>
          <div className="vis vis-editor">
            <span className="ide-cross vis-cross a" aria-hidden="true"></span>
            <span className="ide-cross vis-cross b" aria-hidden="true"></span>
            <div className="ide-bezel vis-main">
              <div className="ide">
                <div className="ide-tabs">
                  <span className="ide-tab on">
                    <svg
                      className="fi y"
                      width="14"
                      height="14"
                      viewBox="0 0 24 24"
                      fill="none"
                      stroke="currentColor"
                      strokeWidth="1.8"
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      aria-hidden="true"
                    >
                      <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
                      <path d="M14 3v5h5M9 13h6M9 17h4" />
                    </svg>
                    <span>loomplane.yaml</span>
                  </span>
                  <span className="ide-tools">YAML</span>
                </div>
                <Code
                  html={
                    '<span class="k">services</span>:\n  <span class="k">api</span>:\n    <span class="k">source</span>: <span class="s">go:services/api</span>\n    <span class="k">kind</span>: <span class="s">http-service</span>\n    <span class="k">port</span>: <span class="v">8080</span>\n    <span class="k">public</span>: <span class="v">true</span>\n    <span class="k">needs</span>: [<span class="s">db</span>, <span class="s">cache</span>]\n    <span class="k">scale</span>: { <span class="k">min</span>: <span class="v">1</span>, <span class="k">max</span>: <span class="v">5</span> }\n    <span class="k">environments</span>:\n      <span class="k">staging</span>:\n        <span class="k">scale</span>: { <span class="k">min</span>: <span class="v">0</span>, <span class="k">max</span>: <span class="v">2</span> }\n      <span class="k">production</span>:\n        <span class="k">region</span>: <span class="s">europe-west1</span>\n\n<span class="k">ignore</span>:\n  <span class="k">resources</span>: [<span class="s">kafka</span>]   <span class="c"># found, not deployed</span>'
                  }
                />
                <div className="ide-status">
                  <span>revision 14</span>
                  <span>schema loomplane.json</span>
                </div>
              </div>
            </div>
          </div>
        </div>

        <div className="feature" id="p-plans">
          <div className="txt">
            <div className="eyebrow">Plans and approvals</div>
            <h3 className="h3">Approval names the revision you reviewed.</h3>
            <p>
              A plan lists every action a deployment would take, in order, with
              the problems that block it and the things worth knowing. An Admin
              approves that exact plan. If the manifest or a cloud changed in
              the meantime, the plan comes back stale and nothing runs.
            </p>
            <ul className="ticks">
              <li>Plans wait up to a week for a decision by default</li>
              <li>
                Scope a plan to some repositories and leave the rest as deployed
              </li>
              <li>
                Deletions are shown like any other change, so nothing disappears
                quietly
              </li>
            </ul>
          </div>
          <div className="vis vis-glow">
            <span className="ide-cross vis-cross a" aria-hidden="true"></span>
            <span className="ide-cross vis-cross b" aria-hidden="true"></span>
            <div className="ide-bezel vis-main">
              <div className="panel">
                <div className="panel-h">
                  <b>Plan 9b21e0 · staging</b>
                  <span className="chip warn">stale</span>
                </div>
                <div className="rows">
                  <div className="row">
                    <div className="l">
                      <span className="dot ok"></span>
                      <div>
                        <b>update api</b>
                        <small>scale min 0, max 2</small>
                      </div>
                    </div>
                    <span className="r2">change</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot bad"></span>
                      <div>
                        <b>remove search</b>
                        <small>service turned off in revision 15</small>
                      </div>
                    </div>
                    <span className="r2">delete</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot run"></span>
                      <div>
                        <b>Blocked: missing permission</b>
                        <small>run.services.update on acme-staging</small>
                      </div>
                    </div>
                    <span className="r2">fix</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot wait"></span>
                      <div>
                        <b>Revision changed</b>
                        <small>made from 14, manifest is now 15</small>
                      </div>
                    </div>
                    <span className="r2">re-plan</span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>

        <div className="feature flip" id="p-deploy">
          <div className="txt">
            <div className="eyebrow">Live deployments</div>
            <h3 className="h3">Watch each step build, deploy and answer.</h3>
            <p>
              Approval starts a durable workflow. You follow every step live in
              the dashboard, and the environment shows what runs where
              afterwards. If a step fails, the retry starts from that step
              instead of the beginning.
            </p>
            <ul className="ticks">
              <li>
                Events and build output are saved before they're shown, so a
                restart loses nothing
              </li>
              <li>
                Live updates over WebSocket, falling back to SSE and then plain
                refresh
              </li>
              <li>A Live badge only when the status heartbeat is recent</li>
              <li>Email and signed webhooks when a deployment finishes</li>
            </ul>
          </div>
          <div className="vis vis-term">
            <span className="ide-cross vis-cross a" aria-hidden="true"></span>
            <span className="ide-cross vis-cross b" aria-hidden="true"></span>
            <div className="ide-bezel vis-main">
              <div className="ide">
                <div className="term-bar">
                  <span className="dots">
                    <i></i>
                    <i></i>
                    <i></i>
                  </span>
                  <span className="term-title">deploy api · cloud-run</span>
                  <span className="term-meta">live</span>
                </div>
                <Code
                  html={
                    '<span class="c">14:02:11</span> build    cloud-build  step 1/4 fetch source\n<span class="c">14:02:19</span> build    cloud-build  step 2/4 go build ./services/api\n<span class="c">14:02:58</span> build    cloud-build  <span class="s">image pushed</span> sha256:8c1f…\n<span class="c">14:03:04</span> deploy   cloud-run    revision api-00014 created\n<span class="c">14:03:22</span> deploy   cloud-run    <span class="w">waiting for ready</span>\n<span class="c">14:03:31</span> check    https://api-xyz.run.app/healthz <span class="s">200</span>\n<span class="c">14:03:31</span> traffic  100% to api-00014\n<span class="c">14:03:32</span> <span class="s">done</span>     api is live · 1m 21s'
                  }
                />
              </div>
            </div>
          </div>
        </div>

        <div className="feature" id="p-drift">
          <div className="txt">
            <div className="eyebrow">Drift</div>
            <h3 className="h3">What runs is what you deployed.</h3>
            <p>
              A scheduled check compares every deployed service and resource
              with what's actually live. When someone scales a service by hand
              or edits it in a console, the difference shows up, and the next
              plan puts it back.
            </p>
            <ul className="ticks">
              <li>
                Every 30 minutes by default, set with{" "}
                <code>LOOMPLANE_DRIFT_INTERVAL</code>
              </li>
              <li>
                Drift shows on the environment and through{" "}
                <code>loomctl status</code>
              </li>
              <li>
                Corrections go through the same review as any other change
              </li>
            </ul>
          </div>
          <div className="vis vis-inspect">
            <span className="ide-cross vis-cross a" aria-hidden="true"></span>
            <span className="ide-cross vis-cross b" aria-hidden="true"></span>
            <div className="ide-bezel vis-main">
              <div className="vis-bar">
                <span className="dots">
                  <i></i>
                  <i></i>
                  <i></i>
                </span>
                <span className="vb-crumb">acme / mono / production</span>
                <span className="chip ok">drift check</span>
              </div>
              <div className="panel">
                <div className="panel-h">
                  <b>production</b>
                  <small>checked 14:30 UTC</small>
                </div>
                <div className="rows">
                  <div className="row">
                    <div className="l">
                      <span className="dot ok"></span>
                      <div>
                        <b>db</b>
                        <small>cloud-sql · postgres 17</small>
                      </div>
                    </div>
                    <span className="chip ok">matches</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot run"></span>
                      <div>
                        <b>api</b>
                        <small>max instances 5 to 12 in the console</small>
                      </div>
                    </div>
                    <span className="chip warn">drifted</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot ok"></span>
                      <div>
                        <b>worker</b>
                        <small>ecs-fargate</small>
                      </div>
                    </div>
                    <span className="chip ok">matches</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot ok"></span>
                      <div>
                        <b>web</b>
                        <small>vercel · acme.dev</small>
                      </div>
                    </div>
                    <span className="chip ok">matches</span>
                  </div>
                </div>
              </div>
              <div className="vis-foot">
                <span className="live" aria-hidden="true"></span>4 checked · 1
                drifted · next check 15:00
              </div>
            </div>
          </div>
        </div>

        <div className="feature flip" id="p-gateways">
          <div className="txt">
            <div className="eyebrow">Gateways and networking</div>
            <h3 className="h3">
              One public address in front of services on any cloud.
            </h3>
            <p>
              Gateways route paths to services, built on Bastion and run on your
              infrastructure. Services find each other through variables. ECS
              services reach each other privately, and private Cloud Run
              services only take calls from the project's own.
            </p>
            <ul className="ticks">
              <li>Path routing with optional prefix stripping</li>
              <li>
                Public addresses only for what serves HTTP, unless you say
                otherwise
              </li>
              <li>Domains you manage alongside the routes that use them</li>
            </ul>
          </div>
          <div className="vis vis-glow">
            <span className="ide-cross vis-cross a" aria-hidden="true"></span>
            <span className="ide-cross vis-cross b" aria-hidden="true"></span>
            <div className="ide-bezel vis-main">
              <div className="panel">
                <div className="panel-h">
                  <b>Gateway edge</b>
                  <small>acme.dev</small>
                </div>
                <div className="rows">
                  <div className="row">
                    <div className="l">
                      <span className="dot blue"></span>
                      <div>
                        <b>/api</b>
                        <small>to api · Cloud Run · prefix stripped</small>
                      </div>
                    </div>
                    <span className="r2">us-central1</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot blue"></span>
                      <div>
                        <b>/</b>
                        <small>to web · Vercel</small>
                      </div>
                    </div>
                    <span className="r2">edge</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot wait"></span>
                      <div>
                        <b>worker</b>
                        <small>private · no route</small>
                      </div>
                    </div>
                    <span className="r2">ECS</span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}
