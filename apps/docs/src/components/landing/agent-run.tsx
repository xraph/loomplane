// One release as the built-in agent carries it, drawn the way the dashboard
// shows a run. The home page and /agents both show it.
const releaseSteps = [
  {
    dot: "ok",
    what: "Planned b81f3c0 for staging",
    detail: "+2 create · 1 update",
    when: "09:12",
  },
  {
    dot: "ok",
    what: "Staging canary reached 100%",
    detail: "no 5xx · p95 steady",
    when: "09:40",
  },
  {
    dot: "ok",
    what: "Staging soaked for 2 hours",
    detail: "project minimum · 1 hour",
    when: "11:40",
  },
  {
    dot: "wait",
    what: "Asked Payments to approve production",
    detail: "request · deployment:approve",
    when: "waiting",
  },
  {
    dot: "blue",
    what: "Production canary, then the watch",
    detail: "rolls back on a breach",
    when: "next",
  },
] as const;

export function AgentRun() {
  return (
    <div className="vis vis-stack">
      <span className="ide-cross vis-cross a" aria-hidden="true"></span>
      <span className="ide-cross vis-cross b" aria-hidden="true"></span>
      <div className="ide-bezel vis-main">
        <div className="panel">
          <div className="panel-h">
            <b>api · release b81f3c0</b>
            <small>Loomplane agent</small>
          </div>
          <div className="rows">
            {releaseSteps.map((r) => (
              <div className="row" key={r.what}>
                <div className="l">
                  <span className={`dot ${r.dot}`}></span>
                  <div>
                    <b>{r.what}</b>
                    <small>{r.detail}</small>
                  </div>
                </div>
                <span className="r2">{r.when}</span>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
