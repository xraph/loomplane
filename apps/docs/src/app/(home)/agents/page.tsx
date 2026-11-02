import type { Metadata } from "next";
import Link from "next/link";
import type { CSSProperties } from "react";
import { Code } from "@/components/landing/code";

export const metadata: Metadata = {
  title: "CLI and agents",
  description: "loomctl for terminals and CI, and an MCP server for AI agents.",
};

export default function AgentsPage() {
  return (
    <>
      <section className="page-hero grain" aria-labelledby="agents-title">
        <div className="wrap">
          <div className="crumb rise">
            <Link href="/">Loomplane</Link> / <b>CLI &amp; agents</b>
          </div>
          <h1 id="agents-title">
            Same platform, from a terminal{" "}
            <span className="thread">or an agent.</span>
          </h1>
          <p className="lede">
            Install it with <code>brew install xraph/tap/loomctl</code>.{" "}
            <code>loomctl</code> does what the dashboard does, for terminals and
            CI. It runs on API keys that act as you, with your role, and can be
            read-only. <code>loomctl mcp</code> turns the same commands into
            tools an AI agent can use.
          </p>
        </div>
      </section>

      <div className="wrap">
        <div className="feature">
          <div className="txt">
            <div className="eyebrow">MCP server</div>
            <h3 className="h3">
              Let an agent plan. Decide whether it can deploy.
            </h3>
            <p>
              Make an API key under Settings, add <code>loomctl mcp</code> to
              your agent's configuration, and it can analyze, write a manifest
              and plan. Approving and retrying need <code>--allow-deploy</code>.
              A manifest that doesn't parse comes back with each problem and its
              line, so the agent can fix it and save again.
            </p>
            <ul className="ticks">
              <li>The manifest schema is a resource the agent can read</li>
              <li>Secrets are set but never shown back</li>
              <li>
                Works with Claude Code:{" "}
                <code>claude mcp add loomplane -- loomctl mcp</code>
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
                      className="fi j"
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
                      <path d="M8 3H7a2 2 0 0 0-2 2v5a2 2 0 0 1-2 2 2 2 0 0 1 2 2v5a2 2 0 0 0 2 2h1M16 3h1a2 2 0 0 1 2 2v5a2 2 0 0 0 2 2 2 2 0 0 0-2 2v5a2 2 0 0 1-2 2h-1" />
                    </svg>
                    <span>.mcp.json</span>
                  </span>
                  <span className="ide-tools">JSON</span>
                </div>
                <Code
                  html={
                    '{\n  <span class="k">"mcpServers"</span>: {\n    <span class="k">"loomplane"</span>: {\n      <span class="k">"command"</span>: <span class="s">"loomctl"</span>,\n      <span class="k">"args"</span>: [<span class="s">"mcp"</span>],\n      <span class="k">"env"</span>: {\n        <span class="k">"LOOMPLANE_URL"</span>: <span class="s">"https://loom.acme.dev"</span>,\n        <span class="k">"LOOMPLANE_API_KEY"</span>: <span class="s">"lp_live_…"</span>\n      }\n    }\n  }\n}'
                  }
                />
                <div className="ide-status">
                  <span>MCP server config</span>
                  <span>2 env vars</span>
                </div>
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
          <div className="eyebrow">Tools</div>
          <h2>What an agent can do.</h2>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Tool</th>
                  <th>What it does</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>
                    <code>whoami</code>
                  </td>
                  <td>Who the key acts as, and its permissions</td>
                </tr>
                <tr>
                  <td>
                    <code>list_projects</code>
                  </td>
                  <td>The organization's projects</td>
                </tr>
                <tr>
                  <td>
                    <code>analyze</code>
                  </td>
                  <td>
                    Reads a project's repositories, or the ones named, and waits
                    until each is done
                  </td>
                </tr>
                <tr>
                  <td>
                    <code>get_manifest</code>, <code>draft_manifest</code>,{" "}
                    <code>save_manifest</code>
                  </td>
                  <td>
                    Reads, drafts and saves <code>loomplane.yaml</code>
                  </td>
                </tr>
                <tr>
                  <td>
                    <code>create_plan</code>, <code>list_plans</code>,{" "}
                    <code>get_plan</code>, <code>reject_plan</code>
                  </td>
                  <td>
                    Plans an environment and reads the result, optionally scoped
                    to some repositories
                  </td>
                </tr>
                <tr>
                  <td>
                    <code>list_deployments</code>, <code>get_deployment</code>
                  </td>
                  <td>Follows deployments step by step</td>
                </tr>
                <tr>
                  <td>
                    <code>status</code>
                  </td>
                  <td>What runs where, with addresses and drift</td>
                </tr>
                <tr>
                  <td>
                    <code>list_variables</code>, <code>set_variable</code>
                  </td>
                  <td>Variables, with secret values never shown</td>
                </tr>
                <tr>
                  <td>
                    <code>approve_plan</code>, <code>retry_deployment</code>
                  </td>
                  <td>
                    Only with <code>--allow-deploy</code>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <div className="wrap">
        <div className="feature flip">
          <div className="txt">
            <div className="eyebrow">CI</div>
            <h3 className="h3">
              Plan on every merge. Approve where you review.
            </h3>
            <p>
              Run <code>loomctl plan</code> in CI so every merge produces a
              plan, and approve it from the dashboard or the terminal. Use a
              read-only key for status checks and a separate key for the people
              who approve.
            </p>
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
                    <span>plan.yml</span>
                  </span>
                  <span className="ide-tools">YAML</span>
                </div>
                <Code
                  html={
                    '<span class="k">on</span>:\n  <span class="k">push</span>: { <span class="k">branches</span>: [<span class="s">main</span>] }\n<span class="k">jobs</span>:\n  <span class="k">plan</span>:\n    <span class="k">runs-on</span>: <span class="s">ubuntu-latest</span>\n    <span class="k">steps</span>:\n      - <span class="k">run</span>: <span class="s">echo "$LOOM_KEY" | loomctl login --url $LOOM_URL --key -</span>\n      - <span class="k">run</span>: <span class="s">loomctl analyze mono</span>\n      - <span class="k">run</span>: <span class="s">loomctl plan mono --env production</span>'
                  }
                />
                <div className="ide-status">
                  <span>.github/workflows</span>
                  <span>on push to main</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}
