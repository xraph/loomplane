import type { Metadata } from "next";
import Link from "next/link";
import type { CSSProperties } from "react";
import { Code } from "@/components/landing/code";

export const metadata: Metadata = {
  title: "Security",
  description:
    "Organization isolation, roles on every action, and where every secret lives.",
};

export default function SecurityPage() {
  return (
    <>
      <section className="page-hero grain" aria-labelledby="security-title">
        <div className="wrap">
          <div className="crumb rise">
            <Link href="/">Loomplane</Link> / <b>Security</b>
          </div>
          <h1 id="security-title">
            Built to be trusted{" "}
            <span className="thread">with your clouds.</span>
          </h1>
          <p className="lede">
            Loomplane holds the keys to your infrastructure, so it's strict
            about who acts, in which organization, and where every secret lives.
            Here's how.
          </p>
        </div>
      </section>

      <div className="wrap">
        <div className="grid" style={{ marginTop: "0" } as CSSProperties}>
          <div className="cell">
            <span className="tag">Tenancy</span>
            <h4>One organization per request</h4>
            <p>
              Your session or API key picks the organization, and nothing in a
              request can change it. Another organization's projects aren't
              found, the same answer as something that doesn't exist.
            </p>
          </div>
          <div className="cell">
            <span className="tag">Roles</span>
            <h4>Checked on every action</h4>
            <p>
              Warden roles decide what each person can do. An API key acts as
              the person who made it, with their role, and loses it when they
              do.
            </p>
          </div>
          <div className="cell">
            <span className="tag">Secrets</span>
            <h4>Encrypted, then handed off</h4>
            <p>
              Credentials and secrets are encrypted with keys derived from your
              secret key, kept in Vault, and written to each cloud's own secret
              store.
            </p>
          </div>
          <div className="cell">
            <span className="tag">Audit</span>
            <h4>Every change recorded</h4>
            <p>
              Chronicle keeps an audit trail of every change in the
              organization: who, what and when.
            </p>
          </div>
          <div className="cell">
            <span className="tag">Analysis</span>
            <h4>Code is read, never run</h4>
            <p>
              Repository analysis is static, in a separate worker. A
              repository's scripts don't execute on Loomplane.
            </p>
          </div>
          <div className="cell">
            <span className="tag">Webhooks</span>
            <h4>Signed notifications</h4>
            <p>
              Owners and admins manage signed webhooks. Deployment mail checks
              the recipient's current membership and project access before it's
              sent.
            </p>
          </div>
        </div>
      </div>

      <section className="band">
        <div className="wrap sec">
          <div className="eyebrow">Who can do what</div>
          <h2>Four roles, nothing in between.</h2>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Role</th>
                  <th>Can</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>Viewer</td>
                  <td>
                    Read everything except secret values, and change nothing
                  </td>
                </tr>
                <tr>
                  <td>Developer</td>
                  <td>
                    Create projects, analyze, edit manifests and variables, and
                    plan
                  </td>
                </tr>
                <tr>
                  <td>Admin</td>
                  <td>
                    Approve plans, retry deployments, manage connections,
                    secrets and members
                  </td>
                </tr>
                <tr>
                  <td>Owner</td>
                  <td>Everything, including the organization itself</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <div className="wrap">
        <div className="feature">
          <div className="txt">
            <div className="eyebrow">Production safety</div>
            <h3 className="h3">It refuses to start unsafely.</h3>
            <p>
              In production, Loomplane won't start without a real secret key and
              an HTTPS public address. Endpoint checks from your targets never
              connect to Loomplane's own host, database or API, and you can deny
              more ranges on top.
            </p>
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
                  <span className="term-title">loomplane</span>
                  <span className="term-meta">production</span>
                </div>
                <Code
                  html={
                    '<span class="k">$</span> LOOMPLANE_ENVIRONMENT=production loomplane\n<span class="r">refusing to start:</span>\n  <span class="r">-</span> LOOMPLANE_SECRET_KEY is the development key\n  <span class="r">-</span> LOOMPLANE_PUBLIC_URL must be https\n\n<span class="c"># 32 random bytes, base64</span>\n<span class="k">$</span> openssl rand -base64 32'
                  }
                />
              </div>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}
