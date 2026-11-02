"use client";

import { type ReactNode, useEffect, useRef, useState } from "react";
import { panes, steps } from "./how-data";

type Pane = "manifest" | "cli" | "drift";

const NAMES: Record<Pane, string> = {
  manifest: "loomplane.yaml",
  cli: "loomctl",
  drift: "drift.log",
};
const BEHIND: Record<Pane, Pane> = {
  manifest: "cli",
  cli: "manifest",
  drift: "cli",
};
const STATUS: Record<Pane, [string, string]> = {
  manifest: ["revision 14 · YAML", "schema loomplane.json"],
  cli: ["zsh · loomctl", "exit 0"],
  drift: ["production · 14:30 UTC", "1 drifted"],
};

const icon = (d: ReactNode, cls: string) => (
  <svg
    className={`fi ${cls}`}
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
    {d}
  </svg>
);
const ICONS: Record<Pane, ReactNode> = {
  manifest: icon(
    <>
      <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
      <path d="M14 3v5h5M9 13h6M9 17h4" />
    </>,
    "y",
  ),
  cli: icon(
    <>
      <rect x="3" y="4" width="18" height="16" rx="2" />
      <path d="m7 9 3 3-3 3M13 15h4" />
    </>,
    "t",
  ),
  drift: icon(<path d="M3 12h4l3-8 4 16 3-8h4" />, "d"),
};

function Lines({ pane, limit }: { pane: Pane; limit?: number }) {
  const lines = panes[pane].split("\n").slice(0, limit);
  return (
    <>
      {lines.map((line, i) => (
        <span
          // biome-ignore lint/suspicious/noArrayIndexKey: static lines
          key={i}
          className="ln"
          // biome-ignore lint/security/noDangerouslySetInnerHtml: trusted first-party markup
          dangerouslySetInnerHTML={{ __html: line }}
        />
      ))}
    </>
  );
}

/** Six steps on the left; an editor on the right that follows them and plays through on its own. */
export function HowItWorks() {
  const [step, setStep] = useState(0);
  const [pane, setPane] = useState<Pane>(steps[0].pane as Pane);
  const [paused, setPaused] = useState(true);
  const flow = useRef<HTMLOListElement>(null);

  const pick = (k: number) => {
    setStep(k);
    setPane(steps[k].pane as Pane);
  };

  useEffect(() => {
    if (matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    const el = flow.current;
    if (!el || !("IntersectionObserver" in window)) {
      setPaused(false);
      return;
    }
    const io = new IntersectionObserver((es) =>
      setPaused(!es[0].isIntersecting),
    );
    io.observe(el);
    return () => io.disconnect();
  }, []);

  const behind = BEHIND[pane];

  return (
    <div className="how">
      <ol
        ref={flow}
        className={paused ? "flow paused" : "flow"}
        aria-label="Deployment steps"
      >
        {steps.map((s, k) => (
          <li key={s.n} className={k === step ? "on" : undefined}>
            <button
              className="flow-hit"
              type="button"
              aria-current={k === step ? "step" : undefined}
              onClick={() => pick(k)}
            >
              <span className="n">{s.n}</span>
              <div>
                <h4>{s.title}</h4>
                <p>{s.body}</p>
              </div>
            </button>
            <span
              className="prog"
              aria-hidden="true"
              onAnimationEnd={() => pick((k + 1) % steps.length)}
            />
          </li>
        ))}
      </ol>

      <div className="ide-wrap">
        <span className="ide-cross" aria-hidden="true" />
        <div className="ide-back ide-bezel" aria-hidden="true">
          <div className="ide">
            <div className="ide-tabs">
              <span className="ide-tab on">
                {ICONS[behind]}
                <span>{NAMES[behind]}</span>
              </span>
            </div>
            <pre>
              <Lines pane={behind} limit={14} />
            </pre>
          </div>
        </div>
        <div className="ide-front ide-bezel">
          <div className="ide">
            <div className="ide-tabs" role="tablist" aria-label="Example file">
              {(Object.keys(NAMES) as Pane[]).map((p) => (
                <button
                  key={p}
                  className="tab ide-tab"
                  type="button"
                  role="tab"
                  aria-selected={p === pane}
                  onClick={() => setPane(p)}
                >
                  {ICONS[p]}
                  <span>{NAMES[p]}</span>
                </button>
              ))}
              <span className="ide-tools">
                <span className="live" aria-hidden="true" />
                synced
              </span>
            </div>
            <pre key={pane}>
              <Lines pane={pane} />
            </pre>
            <div className="ide-status">
              <span>{STATUS[pane][0]}</span>
              <span>{STATUS[pane][1]}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
