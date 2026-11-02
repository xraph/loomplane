import type { Metadata } from "next";
import Link from "next/link";
import type { CSSProperties } from "react";
import { Code } from "@/components/landing/code";

export const metadata: Metadata = {
  title: "Clouds",
  description:
    "Vercel, Cloud Run, ECS, Render, App Platform, Container Apps and your own servers.",
};

export default function CloudsPage() {
  return (
    <>
      <section className="page-hero grain" aria-labelledby="clouds-title">
        <div className="wrap">
          <div className="crumb rise">
            <Link href="/">Loomplane</Link> / <b>Clouds</b>
          </div>
          <h1 id="clouds-title">
            Each service on the cloud{" "}
            <span className="thread">that suits it.</span>
          </h1>
          <p className="lede">
            Connect the accounts you already have. A Google Cloud service
            account key, a Vercel token, an AWS access key, a Render API key, a
            DigitalOcean token, an Azure service principal. Each is checked with
            its cloud before it's kept, and Loomplane tells you which
            permissions are missing.
          </p>
        </div>
      </section>

      <section className="band">
        <div
          className="wrap"
          style={{ paddingBottom: "96px" } as CSSProperties}
        >
          <div className="eyebrow">Targets</div>
          <h2>Where things run, and where they're built.</h2>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Provider</th>
                  <th>Services</th>
                  <th>Databases, caches, buckets</th>
                  <th>Builds on</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>
                    <span className="prov">
                      <svg
                        className="lg"
                        style={{ color: "#4285F4" } as CSSProperties}
                        aria-hidden="true"
                      >
                        <use href="#i-gcp" />
                      </svg>
                      Google Cloud
                    </span>
                  </td>
                  <td className="on">Cloud Run</td>
                  <td>
                    Cloud SQL (PostgreSQL, MySQL), Memorystore, Cloud Storage
                  </td>
                  <td>Cloud Build</td>
                </tr>
                <tr>
                  <td>
                    <span className="prov">
                      <svg
                        className="lg"
                        style={{ color: "var(--ink)" } as CSSProperties}
                        aria-hidden="true"
                      >
                        <use href="#i-aws" />
                      </svg>
                      AWS
                    </span>
                  </td>
                  <td className="on">ECS on Fargate</td>
                  <td>RDS (PostgreSQL, MySQL), ElastiCache, S3</td>
                  <td>CodeBuild</td>
                </tr>
                <tr>
                  <td>
                    <span className="prov">
                      <svg
                        className="lg"
                        style={{ color: "var(--ink)" } as CSSProperties}
                        aria-hidden="true"
                      >
                        <use href="#i-vercel" />
                      </svg>
                      Vercel
                    </span>
                  </td>
                  <td className="on">Web apps, static sites</td>
                  <td>None</td>
                  <td>Vercel</td>
                </tr>
                <tr>
                  <td>
                    <span className="prov">
                      <svg
                        className="lg"
                        style={{ color: "var(--ink)" } as CSSProperties}
                        aria-hidden="true"
                      >
                        <use href="#i-render" />
                      </svg>
                      Render
                    </span>
                  </td>
                  <td className="on">Web services, workers</td>
                  <td>Render Postgres, Key Value</td>
                  <td>Render</td>
                </tr>
                <tr>
                  <td>
                    <span className="prov">
                      <svg
                        className="lg"
                        style={{ color: "#3D9BFF" } as CSSProperties}
                        aria-hidden="true"
                      >
                        <use href="#i-digitalocean" />
                      </svg>
                      DigitalOcean
                    </span>
                  </td>
                  <td className="on">App Platform</td>
                  <td>Managed PostgreSQL, MySQL, Valkey</td>
                  <td>App Platform</td>
                </tr>
                <tr>
                  <td>Azure</td>
                  <td className="on">Container Apps</td>
                  <td>
                    Azure Database for PostgreSQL and MySQL, Azure Managed Redis
                  </td>
                  <td>ACR Tasks</td>
                </tr>
                <tr>
                  <td>
                    <span className="prov">
                      <svg
                        className="lg"
                        style={{ color: "#2496ED" } as CSSProperties}
                        aria-hidden="true"
                      >
                        <use href="#i-docker" />
                      </svg>
                      Your servers
                    </span>
                  </td>
                  <td className="on">SSH with Docker, Kubernetes namespaces</td>
                  <td>The service catalog's engines</td>
                  <td>Your registry</td>
                </tr>
                <tr>
                  <td>Anything else</td>
                  <td className="on">A provider plugin</td>
                  <td>A provider plugin</td>
                  <td>Your choice</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <div className="wrap">
        <div className="feature">
          <div className="txt">
            <div className="eyebrow">Cloud targets</div>
            <h3 className="h3">Pick a target by name, not by account ID.</h3>
            <p>
              Group accounts into a Cloud, give each target a name like{" "}
              <code>frontend</code>, and assign the Cloud to an environment.
              Your manifest refers to <code>frontend</code>, and production and
              staging each resolve it to their own account.
            </p>
            <ul className="ticks">
              <li>
                One connection per cloud per environment, shared by its services
              </li>
              <li>With one ready account, Loomplane picks it for you</li>
              <li>
                Account choices are saved on the server and used by the
                dashboard, CLI and API
              </li>
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
                  <span className="term-title">loomctl · clouds</span>
                  <span className="term-meta">zsh</span>
                </div>
                <Code
                  html={
                    '<span class="k">services</span>:\n  <span class="k">web</span>:\n    <span class="k">source</span>: <span class="s">node:apps/web</span>\n    <span class="k">cloud_target</span>: <span class="s">frontend</span>\n\n<span class="k">$</span> loomctl clouds create mono --name Production\n<span class="k">$</span> loomctl cloud-targets create mono cloud_7d2 \\\n    --name frontend --provider vercel --connection conn_41a\n<span class="k">$</span> loomctl clouds assign mono env_prod --cloud cloud_7d2 \\\n    --revision 0 --wait --timeout 10m\n<span class="s">  assigned · web resolves to vercel/acme-prod</span>'
                  }
                />
              </div>
            </div>
          </div>
        </div>

        <div className="feature flip">
          <div className="txt">
            <div className="eyebrow">Adopt and move</div>
            <h3 className="h3">
              Bring what already runs. Move it when you're ready.
            </h3>
            <p>
              Adopt an existing service by describing where it runs today. When
              the placement matches what already serves, the plan records it
              without redeploying anything. Later, replace the account or switch
              providers, and the plan shows exactly what moves.
            </p>
            <ul className="ticks">
              <li>
                Adoption that changes nothing when nothing needs to change
              </li>
              <li>Follow and recover a sync if a provider call goes missing</li>
              <li>Finish external routing or revert it, step by step</li>
              <li>
                A Cloud switch never moves database contents behind your back
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
                  <b>Move web</b>
                  <small>vercel/acme-old to vercel/acme-prod</small>
                </div>
                <div className="rows">
                  <div className="row">
                    <div className="l">
                      <span className="dot ok"></span>
                      <div>
                        <b>Stage on new account</b>
                        <small>build and deploy, no traffic</small>
                      </div>
                    </div>
                    <span className="chip ok">done</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot ok"></span>
                      <div>
                        <b>Check new address</b>
                        <small>GET / 200 in 84ms</small>
                      </div>
                    </div>
                    <span className="chip ok">done</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot run"></span>
                      <div>
                        <b>Switch routing</b>
                        <small>acme.dev to the new deployment</small>
                      </div>
                    </div>
                    <span className="chip warn">waiting</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot wait"></span>
                      <div>
                        <b>Retire old deployment</b>
                        <small>after routing is confirmed</small>
                      </div>
                    </div>
                    <span className="chip">later</span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>

        <div className="feature">
          <div className="txt">
            <div className="eyebrow">Servers, pools and plugins</div>
            <h3 className="h3">Your own machines are a target too.</h3>
            <p>
              Connect an SSH server with Docker or a Kubernetes namespace, group
              servers into pools, and share them across projects. When you need
              a target Loomplane doesn't ship, write a provider plugin in Go,
              run it, and point Loomplane at it.
            </p>
            <ul className="ticks">
              <li>
                Storage inventory for every Docker volume and Kubernetes claim
              </li>
              <li>Pause a target to stop new work landing on it</li>
              <li>A provider protocol with its own test suite</li>
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
                <span className="vb-crumb">servers / pools</span>
                <span className="chip ok">healthy</span>
              </div>
              <div className="panel">
                <div className="panel-h">
                  <b>Pool eu-shared</b>
                  <small>3 servers</small>
                </div>
                <div className="rows">
                  <div className="row">
                    <div className="l">
                      <span className="dot ok"></span>
                      <div>
                        <b>hetzner-fsn-1</b>
                        <small>ssh · docker 27 · 6 services</small>
                      </div>
                    </div>
                    <span className="chip ok">ready</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot ok"></span>
                      <div>
                        <b>hetzner-fsn-2</b>
                        <small>ssh · docker 27 · 4 services</small>
                      </div>
                    </div>
                    <span className="chip ok">ready</span>
                  </div>
                  <div className="row">
                    <div className="l">
                      <span className="dot run"></span>
                      <div>
                        <b>k8s-apps</b>
                        <small>kubernetes · namespace apps</small>
                      </div>
                    </div>
                    <span className="chip warn">paused</span>
                  </div>
                </div>
              </div>
              <div className="vis-foot">
                <span className="live" aria-hidden="true"></span>10 services · 2
                ready · 1 paused
              </div>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}
