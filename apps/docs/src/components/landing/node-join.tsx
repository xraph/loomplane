import { Code } from "./code";

// Joining a machine as a node, in a terminal. The home page and /clouds both
// show it; Effects types it out as it scrolls into view.
const session =
  '<span class="c"># On a laptop, a GPU box or a VM anywhere</span>\n' +
  '<span class="k">$</span> sudo loomctl node join --allow probe,docker\n' +
  '<span class="s">  minted a single-use join token</span>\n' +
  '<span class="s">  made an Ed25519 key, the private half stays here</span>\n' +
  '<span class="s">  joined as build-box, dialing out to loom.acme.dev</span>\n' +
  '<span class="s">  service loomplane-node is running</span>\n' +
  "\n" +
  '<span class="k">$</span> sudo loomctl node status\n' +
  '<span class="s">  build-box · online · allows probe, docker</span>\n' +
  "\n" +
  '<span class="c"># No loomctl yet? The dashboard gives you one line</span>\n' +
  '<span class="k">$</span> curl -fsSL https://loom.acme.dev/install/node.sh \\\n' +
  "    | LOOMPLANE_JOIN_TOKEN=lpj_… sh -s -- \\\n" +
  "      --allow probe,docker";

export function NodeJoin() {
  return (
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
            <span className="term-title">build-box</span>
            <span className="term-meta">loomctl node</span>
          </div>
          <Code html={session} />
        </div>
      </div>
    </div>
  );
}
