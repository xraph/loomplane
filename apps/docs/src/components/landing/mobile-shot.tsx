import type { CSSProperties } from "react";

// The app's Watch screen, drawn from the mobile mocks, with the agent asking
// for a production approval over it. Each dot is an hour of a service's traffic.
const services = [
  {
    name: "web",
    where: "Production · Vercel",
    state: "Live",
    tone: "ok",
    hours: "oooooooooooooooooooooooo",
    p95: "94ms",
    rps: "38/s",
  },
  {
    name: "api",
    where: "Production · Cloud Run",
    state: "Canary",
    tone: "blue",
    hours: "oooooooooooooooooooowooo",
    p95: "188ms",
    rps: "12/s",
  },
  {
    name: "worker",
    where: "Staging · DigitalOcean",
    state: "Failed",
    tone: "bad",
    hours: "........oooooooooooooowb",
    p95: "640ms",
    rps: "2/s",
  },
] as const;

const hourClass = { o: "ok", w: "warn", b: "bad", ".": "none" } as const;

export function MobileShot() {
  return (
    <div className="ph-stage">
      <div
        className="ph"
        role="img"
        aria-label="The Loomplane app's Watch screen"
      >
        <div className="ph-screen" aria-hidden="true">
          <div className="ph-head">
            <span className="ph-org">
              <i>A</i>
              <span>
                <b>Acme Inc</b>
                <small>deploy.acme.dev</small>
              </span>
            </span>
            <span className="ph-avatar">RR</span>
          </div>

          <div className="ph-agent">
            <i />
            <span>
              <b>Agent is watching</b> 3 environments
            </span>
          </div>

          <div className="ph-card ph-wait">
            <small>Waiting on you · 1</small>
            <b>api to Production</b>
            <span>
              From the agent · <em>+2 create</em> · 1 update
            </span>
          </div>

          <div className="ph-label">Environments</div>
          {services.map((s) => (
            <div className="ph-card ph-env" key={s.name}>
              <div className="ph-env-top">
                <span>
                  <b>{s.name}</b>
                  <small>{s.where}</small>
                </span>
                <span className={`chip ${s.tone}`}>{s.state}</span>
              </div>
              <div className="ph-dots">
                {s.hours.split("").map((h, i) => (
                  <i
                    // biome-ignore lint/suspicious/noArrayIndexKey: a fixed strip of hours
                    key={i}
                    className={hourClass[h as keyof typeof hourClass]}
                  />
                ))}
              </div>
              <div className="ph-nums">
                <span>
                  <small>p95</small>
                  {s.p95}
                </span>
                <span>
                  <small>Requests</small>
                  {s.rps}
                </span>
              </div>
            </div>
          ))}

          <div className="ph-tabs">
            <span className="on">Watch</span>
            <span>Inbox</span>
            <span>Projects</span>
            <span>More</span>
          </div>
        </div>
      </div>

      <div
        className="ph-push rise"
        style={{ "--d": ".1s" } as CSSProperties}
        aria-label="The agent asks you to approve a production release"
        role="img"
      >
        <div className="ph-push-body" aria-hidden="true">
          <i className="ph-push-icon" />
          <span>
            <small>
              <b>Agent · Needs approval</b>
              <span>now</span>
            </small>
            <b>api to Production</b>
            <span>
              Staging ran b81f3c0 for 2 days with no errors. I planned the same
              revision for Production.
            </span>
          </span>
        </div>
        <div className="ph-push-acts" aria-hidden="true">
          <span>Approve</span>
          <span>Review plan</span>
        </div>
      </div>
    </div>
  );
}
