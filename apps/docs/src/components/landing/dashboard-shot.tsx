'use client';

import {
  Activity,
  Bell,
  Box,
  ChevronRight,
  Cloud,
  CreditCard,
  Database,
  Keyboard,
  LayoutDashboard,
  LayoutGrid,
  Moon,
  PanelLeft,
  Plus,
  Rocket,
  Search,
  Server,
  Settings2,
  Workflow,
} from 'lucide-react';
import { type CSSProperties, type ReactNode, useEffect, useState } from 'react';

/*
 * The dashboard's Overview, redrawn for the hero. It plays the product's own
 * loop: a plan waits for approval, gets approved, deploys, and lands in the
 * activity feed, the deployments chart and the last release card.
 */

type Phase = 'waiting' | 'deploying' | 'live';

const WEEKS = ['W31', 'W32', 'W33', 'W34', 'W35', 'W36', 'W37', 'W38', 'W39', 'W40', 'W41', 'W42'];
const WEEKLY = [3, 5, 4, 6, 5, 7, 6, 8, 7, 9, 11, 4];
const PLAN_TO_LIVE = [0, 0, 0, 0, 9, 9, 0, 0, 0, 5, 0, 0, 0, 0];
const STEPS = 7;
const PITCH = 7;

const projects = [
  { name: 'mono', tone: 'rose', icon: <Workflow /> },
  { name: 'billing', tone: 'ember', icon: <CreditCard /> },
  { name: 'game-framework', tone: 'gold', icon: <Box /> },
];

function Glyph({ tone, children }: { tone: string; children: ReactNode }) {
  return <span className={`ds-glyph ${tone}`}>{children}</span>;
}

function Logo({ id }: { id: string }) {
  return (
    <svg className="ds-logo" aria-hidden="true">
      <use href={`#i-${id}`} />
    </svg>
  );
}

/** Weekly deploys as two-dot columns, the latest week lit in the primary color. */
function DotBars({ data }: { data: number[] }) {
  const w = 360;
  const h = 96;
  const col = w / data.length;
  const max = Math.max(...data);
  return (
    <svg className="ds-chart" viewBox={`0 0 ${w} ${h + 18}`} aria-hidden="true">
      {data.map((v, i) => {
        const rows = Math.max(1, Math.round((v / max) * (h / PITCH)));
        const cx = col * i + col / 2;
        const lit = i === data.length - 1;
        return (
          // biome-ignore lint/suspicious/noArrayIndexKey: fixed weeks
          <g key={i}>
            {Array.from({ length: rows }, (_, k) =>
              [-PITCH / 2, PITCH / 2].map((dx) => (
                <circle
                  // biome-ignore lint/suspicious/noArrayIndexKey: fixed dot positions
                  key={`${k}${dx}`}
                  cx={cx + dx}
                  cy={h - PITCH / 2 - k * PITCH}
                  r={2.1}
                  className={lit ? 'lit' : 'dim'}
                />
              )),
            )}
            <text x={cx} y={h + 15} textAnchor="middle">
              {WEEKS[i]}
            </text>
          </g>
        );
      })}
    </svg>
  );
}

/** Days from plan to live: quiet dashes, with bars on the days something shipped. */
function PlanBars({ data }: { data: number[] }) {
  const w = 360;
  const h = 96;
  const col = w / data.length;
  const max = Math.max(...data);
  return (
    <svg className="ds-chart" viewBox={`0 0 ${w} ${h + 18}`} aria-hidden="true">
      <defs>
        <linearGradient id="ds-bar" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="currentColor" stopOpacity=".55" />
          <stop offset="1" stopColor="currentColor" stopOpacity=".06" />
        </linearGradient>
      </defs>
      {data.map((v, i) => {
        const x = col * i + col * 0.22;
        const bw = col * 0.56;
        if (!v)
          // biome-ignore lint/suspicious/noArrayIndexKey: fixed days
          return <rect key={i} x={x} y={h - 2} width={bw} height={2} rx={1} className="dash" />;
        const bh = (v / max) * (h - 16);
        // biome-ignore lint/suspicious/noArrayIndexKey: fixed days
        return <rect key={i} x={x} y={h - bh} width={bw} height={bh} fill="url(#ds-bar)" />;
      })}
      <text x={0} y={h + 15}>
        Oct 10
      </text>
      <text x={w} y={h + 15} textAnchor="end">
        Oct 23
      </text>
    </svg>
  );
}

export function DashboardShot() {
  const [phase, setPhase] = useState<Phase>('waiting');
  const [step, setStep] = useState(0);
  const [secs, setSecs] = useState(0);
  const [tilt, setTilt] = useState(9);

  // The shot eases flat as you scroll.
  useEffect(() => {
    if (matchMedia('(prefers-reduced-motion: reduce)').matches) {
      setTilt(0);
      return;
    }
    const onScroll = () => setTilt(Math.max(0, 9 - window.scrollY / 45));
    onScroll();
    window.addEventListener('scroll', onScroll, { passive: true });
    return () => window.removeEventListener('scroll', onScroll);
  }, []);

  // waiting (3.5s) -> deploying, one step every 1.4s -> live (5s) -> waiting
  useEffect(() => {
    if (matchMedia('(prefers-reduced-motion: reduce)').matches) return;
    let t: ReturnType<typeof setTimeout>;
    let tick: ReturnType<typeof setInterval> | undefined;
    const waiting = () => {
      setPhase('waiting');
      setStep(0);
      setSecs(0);
      t = setTimeout(deploying, 3500);
    };
    const deploying = () => {
      setPhase('deploying');
      let s = 0;
      let k = 0;
      tick = setInterval(() => {
        if (document.hidden) return;
        s += 1;
        setSecs(s);
        if (s % 2 === 0) {
          k += 1;
          setStep(k);
          if (k >= STEPS) {
            clearInterval(tick);
            setPhase('live');
            t = setTimeout(waiting, 5000);
          }
        }
      }, 700);
    };
    waiting();
    return () => {
      clearTimeout(t);
      clearInterval(tick);
    };
  }, []);

  const shipped = phase === 'live';
  const weekly = shipped ? [...WEEKLY.slice(0, -1), WEEKLY[WEEKLY.length - 1] + 1] : WEEKLY;
  const clock = `${Math.floor(secs / 60)}m ${String(secs % 60).padStart(2, '0')}s`;

  const activity = [
    ...(shipped ? [{ dot: 'ok', text: <>You deployed <b>mono</b> to production</>, when: 'just now', fresh: true }] : []),
    ...(phase !== 'waiting'
      ? [{ dot: 'blue', text: <>You approved the plan for <b>mono</b> to production</>, when: 'just now', fresh: phase === 'deploying' }]
      : []),
    { dot: 'ok', text: <>You deployed <b>billing</b> to staging</>, when: 'yesterday' },
    { dot: 'bad', text: <>A deploy of <b>game-framework</b> to preview failed</>, when: 'yesterday' },
    { dot: 'blue', text: <>Ada approved the plan for <b>billing</b> to staging</>, when: '2d ago' },
    { dot: 'wait', text: <>The plan for <b>mono</b> to staging was superseded</>, when: '2d ago' },
  ].slice(0, 5);

  return (
    <div className="shot rise" style={{ '--d': '.42s' } as CSSProperties}>
      <div className="frame" style={{ '--tilt': `${tilt.toFixed(2)}deg` } as CSSProperties}>
        <div
          className="app ds"
          role="img"
          aria-label="The Loomplane dashboard Overview: a plan waits for approval, gets approved, deploys to production and shows up in activity"
        >
          <nav className="ds-rail" aria-hidden="true">
            <span className="ds-brand">
              <svg viewBox="0 0 32 32" aria-hidden="true">
                <use href="#mark" />
              </svg>
            </span>
            <span className="ds-ico on">
              <LayoutDashboard />
            </span>
            <span className="ds-ico">
              <Rocket />
            </span>
            <span className="ds-ico">
              <LayoutGrid />
            </span>
            <span className="ds-ico">
              <Database />
            </span>
            <span className="ds-ico">
              <Cloud />
            </span>
            <span className="ds-ico">
              <Server />
            </span>
            <span className="ds-ico">
              <Activity />
            </span>
            <span className="ds-ico">
              <Settings2 />
            </span>
            <span className="ds-sep" />
            {projects.map((p) => (
              <Glyph key={p.name} tone={p.tone}>
                {p.icon}
              </Glyph>
            ))}
            <span className="ds-avatar">AL</span>
          </nav>

          <div className="ds-body">
            <header className="ds-top">
              <PanelLeft className="ds-i" />
              <span className="ds-crumb">
                Acme Labs <ChevronRight className="ds-i sm" /> <b>Overview</b>
              </span>
              <span className="ds-search">
                <Search className="ds-i sm" /> Search or run a command <kbd>⌘</kbd>
                <kbd>K</kbd>
              </span>
              <Moon className="ds-i" />
              <Bell className="ds-i" />
              <Keyboard className="ds-i" />
            </header>

            <div className="ds-page">
              <div className="ds-hello">
                <div>
                  <h3>Good morning, Ada</h3>
                  <p>
                    {phase === 'waiting'
                      ? 'One plan is waiting for your approval.'
                      : phase === 'deploying'
                        ? 'mono is deploying to production.'
                        : 'Everything that should be live is live.'}
                  </p>
                </div>
                <span className="ds-new">
                  <Plus className="ds-i sm" /> New deployment <kbd>N</kbd>
                </span>
              </div>

              <div className="ds-row r1">
                <section className="ds-card">
                  <div className="ds-eyebrow">Waiting on you · {phase === 'waiting' ? 1 : 0}</div>
                  {phase === 'waiting' ? (
                    <>
                      <h4>mono to production</h4>
                      <p>Revision 14 · 7 actions · nothing blocking. Planned by Sam 4m ago.</p>
                      <div className="ds-actions">
                        <span className="ds-btn primary">Approve</span>
                        <span className="ds-btn">Review plan</span>
                      </div>
                    </>
                  ) : (
                    <>
                      <h4>Nothing needs your approval</h4>
                      <p>Plans that change an environment wait here until someone approves them.</p>
                      <div className="ds-actions">
                        <span className="ds-btn">Plan a deployment</span>
                      </div>
                    </>
                  )}
                </section>
                <section className="ds-card">
                  <div className="ds-eyebrow">
                    Last release
                    <span className="chip ok">Live</span>
                  </div>
                  <div className="ds-release">
                    <Glyph tone={shipped ? 'rose' : 'ember'}>{shipped ? <Workflow /> : <CreditCard />}</Glyph>
                    <div>
                      <h4>{shipped ? 'mono' : 'billing'}</h4>
                      <p>
                        {shipped ? 'Production' : 'Staging'} <code>{shipped ? '7f3a2c1' : '71eae66'}</code>{' '}
                        {shipped ? 'Finished just now by you' : 'Finished yesterday by you'}
                      </p>
                    </div>
                  </div>
                  <div className="ds-actions">
                    <span className="ds-btn">View deploy</span>
                    <span className="ds-btn">Redeploy</span>
                  </div>
                </section>
              </div>

              <div className="ds-row r2">
                <section className="ds-card">
                  <div className="ds-head">
                    <b>Deployments</b>
                    <small>Last 12 weeks</small>
                  </div>
                  <div className="ds-big">
                    {weekly.reduce((a, b) => a + b, 0)} <small>{weekly[weekly.length - 1]} this week · 2 failed</small>
                  </div>
                  <DotBars data={weekly} />
                </section>
                <section className="ds-card">
                  <div className="ds-head">
                    <b>Plan to live</b>
                    <small>Last 14 days</small>
                  </div>
                  <div className="ds-big">
                    12s <small>median, approval to live</small>
                  </div>
                  <PlanBars data={PLAN_TO_LIVE} />
                </section>
                <section className="ds-card dashed">
                  <div className="ds-head">
                    <b>Spend, latency and traffic</b>
                  </div>
                  <p className="ds-note">
                    These show up once Loomplane reads billing and metrics from your cloud accounts.
                  </p>
                  <dl className="ds-kv">
                    <dt>Cloud spend</dt>
                    <dd>Not connected</dd>
                    <dt>p95 latency</dt>
                    <dd>Not connected</dd>
                    <dt>Traffic</dt>
                    <dd>Not connected</dd>
                  </dl>
                  <span className="ds-link">Connect metrics</span>
                </section>
              </div>

              <div className="ds-row r3">
                <section className="ds-card flush">
                  <div className="ds-head pad">
                    <b>Activity</b>
                    <span className="ds-link">See all</span>
                  </div>
                  <ul className="ds-feed">
                    {activity.map((a) => (
                      <li key={a.when + String(a.dot) + (a.fresh ? 'n' : '')} className={a.fresh ? 'fresh' : undefined}>
                        <span className={`dot ${a.dot}`} />
                        <span className="ds-feed-t">{a.text}</span>
                        <small>{a.when}</small>
                      </li>
                    ))}
                  </ul>
                </section>
                <div className="ds-col">
                  <section className="ds-card">
                    <div className="ds-head">
                      <b>Quick deploy</b>
                    </div>
                    <div className="ds-chips">
                      {projects.map((p) => (
                        <span key={p.name} className="ds-chip">
                          <Glyph tone={p.tone}>{p.icon}</Glyph>
                          {p.name}
                        </span>
                      ))}
                    </div>
                  </section>
                  <section className="ds-card flush">
                    <div className="ds-head pad">
                      <b>
                        Clouds <small>4</small>
                      </b>
                      <span className="ds-link">Manage</span>
                    </div>
                    <ul className="ds-clouds">
                      <li>
                        <Logo id="gcp" />
                        <div>
                          <b>Google Cloud</b>
                          <small>Cloud Run · acme-prod</small>
                        </div>
                        <small>3 services</small>
                      </li>
                      <li>
                        <Logo id="vercel" />
                        <div>
                          <b>Vercel</b>
                          <small>Vercel · Acme</small>
                        </div>
                        <small>1 service</small>
                      </li>
                    </ul>
                  </section>
                </div>
              </div>
            </div>

            <footer className="ds-status">
              <span>
                <i className="dot ok" /> Live
              </span>
              <span>
                <i className="dot ok" /> acme-labs
              </span>
              <span>
                <i className="dot ok" /> 4 accounts
              </span>
              <span className={phase === 'deploying' ? 'busy' : undefined}>
                <i className={phase === 'deploying' ? 'dot run' : 'dot wait'} />
                {phase === 'deploying' ? `Deploying mono · step ${Math.min(step + 1, STEPS)} of ${STEPS} · ${clock}` : 'Nothing deploying'}
              </span>
              <span>
                <i className="dot ok" /> GitHub connected
              </span>
              <span className="ds-status-r">⌘K Command menu</span>
            </footer>
          </div>
        </div>
      </div>
    </div>
  );
}
