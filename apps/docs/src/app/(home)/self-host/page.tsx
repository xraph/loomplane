import type { Metadata } from "next";
import Link from "next/link";
import type { CSSProperties } from "react";
import { Code } from "@/components/landing/code";

export const metadata: Metadata = {
  title: "Self-host",
  description:
    "Run Loomplane on one Docker host with HTTPS, backups and safe upgrades.",
};

export default function SelfHostPage() {
  return (
    <>
      <section className="page-hero grain" aria-labelledby="selfhost-title">
        <div className="wrap">
          <div className="crumb rise">
            <Link href="/">Loomplane</Link> / <b>Self-host</b>
          </div>
          <h1 id="selfhost-title">
            Running in minutes, <span className="thread">yours for good.</span>
          </h1>
          <p className="lede">
            You need a Linux host with Docker Engine and Compose 2.24.4 or
            newer, and a hostname pointed at it. Start with 2 CPU cores and 8 GB
            of RAM. The <code>loomctl</code> CLI installs with Homebrew or
            Scoop.
          </p>
        </div>
      </section>

      <div className="wrap">
        <div className="feature" style={{ paddingTop: "0" } as CSSProperties}>
          <div className="txt">
            <div className="eyebrow">Install</div>
            <h3 className="h3">Production on one Docker host.</h3>
            <p>
              Download the Docker bundle from a GitHub release, check it against{" "}
              <code>SHA256SUMS</code> and extract it into{" "}
              <code>/srv/loomplane</code>. Set <code>LOOMPLANE_IMAGE</code>,{" "}
              <code>LOOMPLANE_DOMAIN</code> and <code>ACME_EMAIL</code> in{" "}
              <code>.env</code>, then start the stack. Open your hostname and
              create your account.
            </p>
            <ul className="ticks">
              <li>
                The API and dashboard, the analysis worker, Postgres 17 and
                Caddy for HTTPS
              </li>
              <li>
                Containers run as UID 10001 on a read-only root filesystem
              </li>
              <li>
                AMD64 and ARM64 images on GHCR with provenance and SBOM
                attestations
              </li>
              <li>
                Working on Loomplane itself? <code>make dev</code> gives you hot
                reload
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
                  <span className="term-title">/srv/loomplane</span>
                  <span className="term-meta">docker compose</span>
                </div>
                <Code
                  html={
                    '<span class="k">$</span> umask 077\n<span class="k">$</span> cp .env.example .env\n<span class="k">$</span> openssl rand -base64 32   <span class="c"># LOOMPLANE_SECRET_KEY</span>\n<span class="k">$</span> openssl rand -hex 32      <span class="c"># POSTGRES_PASSWORD</span>\n\n<span class="k">$</span> docker compose config --quiet\n<span class="k">$</span> docker compose pull\n<span class="k">$</span> docker compose up -d --wait --wait-timeout 240\n<span class="s">  api, worker, postgres and caddy are healthy</span>\n\n<span class="c"># The CLI, on your laptop</span>\n<span class="k">$</span> brew install xraph/tap/loomctl'
                  }
                />
              </div>
            </div>
          </div>
        </div>
      </div>

      <section
        className="band"
        style={{ borderTop: "1px solid var(--line)" } as CSSProperties}
      >
        <div className="wrap sec">
          <div className="eyebrow">Configuration</div>
          <h2>The settings that matter.</h2>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Variable</th>
                  <th>Default</th>
                  <th>What it does</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>
                    <code>LOOMPLANE_DATABASE_URL</code>
                  </td>
                  <td>required</td>
                  <td>
                    PostgreSQL. Falls back to <code>DATABASE_URL</code>, which
                    App Platform and Render set for you
                  </td>
                </tr>
                <tr>
                  <td>
                    <code>LOOMPLANE_SECRET_KEY</code>
                  </td>
                  <td>development key</td>
                  <td>
                    32 random bytes, base64. Every credential and secret is
                    encrypted with keys derived from it
                  </td>
                </tr>
                <tr>
                  <td>
                    <code>LOOMPLANE_PUBLIC_URL</code>
                  </td>
                  <td>
                    <code>http://localhost:8780</code>
                  </td>
                  <td>Where people reach Loomplane; https in production</td>
                </tr>
                <tr>
                  <td>
                    <code>LOOMPLANE_ENVIRONMENT</code>
                  </td>
                  <td>
                    <code>development</code>
                  </td>
                  <td>
                    <code>development</code>, <code>staging</code> or{" "}
                    <code>production</code>
                  </td>
                </tr>
                <tr>
                  <td>
                    <code>LOOMPLANE_PLANNING_APPROVAL_WINDOW</code>
                  </td>
                  <td>
                    <code>168h</code>
                  </td>
                  <td>How long a plan waits for a decision</td>
                </tr>
                <tr>
                  <td>
                    <code>LOOMPLANE_DRIFT_INTERVAL</code>
                  </td>
                  <td>
                    <code>30m</code>
                  </td>
                  <td>
                    How often deployments are checked for drift; <code>0</code>{" "}
                    turns it off
                  </td>
                </tr>
                <tr>
                  <td>
                    <code>LOOMPLANE_GITHUB_*</code>
                  </td>
                  <td>unset</td>
                  <td>The GitHub App for private repositories</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <div className="wrap">
        <div className="grid" style={{ marginBlock: "96px" } as CSSProperties}>
          <div className="cell">
            <span className="tag">Backups</span>
            <h4>Back up Loomplane itself</h4>
            <p>
              One PostgreSQL database holds everything. Back it up, restore it,
              and keep the secret key away from those backups.
            </p>
          </div>
          <div className="cell">
            <span className="tag">Upgrades</span>
            <h4>Safe upgrades</h4>
            <p>
              One migration pass over every library's schema, and a schema guard
              that refuses to start against a database it doesn't understand.
            </p>
          </div>
          <div className="cell">
            <span className="tag">Metrics</span>
            <h4>Prometheus and health</h4>
            <p>
              Metrics and health checks for the API and the analysis worker, so
              you can watch Loomplane like any other service.
            </p>
          </div>
          <div className="cell">
            <span className="tag">Realtime</span>
            <h4>Proxy-friendly live state</h4>
            <p>
              Proxy <code>/api/v1/realtime/ws</code> with WebSocket upgrades and
              leave the SSE route unbuffered. Replicas share one durable event
              sequence.
            </p>
          </div>
          <div className="cell">
            <span className="tag">Email</span>
            <h4>SMTP when you want it</h4>
            <p>
              An empty SMTP host turns email off, and invitations show a link
              you can send yourself.
            </p>
          </div>
          <div className="cell">
            <span className="tag">Architecture</span>
            <h4>One process and a worker</h4>
            <p>
              The same Go process serves the dashboard and API and runs
              deployment workflows. The worker analyzes repositories.
            </p>
          </div>
        </div>
      </div>
    </>
  );
}
