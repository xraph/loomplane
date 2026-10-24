import type { CSSProperties } from 'react';
import type { Metadata } from 'next';
import Link from 'next/link';
import { DashboardShot } from '@/components/landing/dashboard-shot';
import { HeroCta } from '@/components/landing/hero-cta';
import { HowItWorks } from '@/components/landing/how-it-works';

export const metadata: Metadata = {
  title: { absolute: 'Loomplane' },
  description: "A self-hosted deployment platform for the monorepos you already have.",
};

export default function HomePage() {
  return (
    <>

    <section className="page-hero grain" aria-labelledby="home-title">
      <div className="wrap" style={{ paddingBlock: "104px 0" } as CSSProperties}>
        <h1 id="home-title" className="rise" style={{ "--d": ".08s" } as CSSProperties}>Your infrastructure, <span className="thread">woven together.</span></h1>
        <p className="lede rise" style={{ "--d": ".18s" } as CSSProperties}>Give Loomplane your monorepo, or an application split across several repositories. It reads the code (never runs it), finds the services and the databases they need, drafts a <code>loomplane.yaml</code> and shows you the exact plan. Approve it, and a Next.js app goes to Vercel, a Go API to Cloud Run, a worker to ECS on Fargate and Postgres to Cloud SQL, wired together.</p>

        <HeroCta />

        <DashboardShot />
      </div>
    </section>

    <div className="marquee">
      <div className="marquee-track">
        <span><svg className="lg" aria-hidden="true"><use href="#i-vercel" /></svg>Vercel</span><span><svg className="lg" aria-hidden="true"><use href="#i-gcp" /></svg>Google Cloud</span><span><svg className="lg" aria-hidden="true"><use href="#i-aws" /></svg>AWS</span><span><svg className="lg" aria-hidden="true"><use href="#i-render" /></svg>Render</span><span><svg className="lg" aria-hidden="true"><use href="#i-digitalocean" /></svg>DigitalOcean</span><span><i></i>Azure</span><span><svg className="lg" aria-hidden="true"><use href="#i-docker" /></svg>Docker</span><span><svg className="lg" aria-hidden="true"><use href="#i-kubernetes" /></svg>Kubernetes</span><span><svg className="lg" aria-hidden="true"><use href="#i-postgresql" /></svg>PostgreSQL</span><span><svg className="lg" aria-hidden="true"><use href="#i-mysql" /></svg>MySQL</span><span><svg className="lg" aria-hidden="true"><use href="#i-mariadb" /></svg>MariaDB</span><span><svg className="lg" aria-hidden="true"><use href="#i-redis" /></svg>Redis</span><span><svg className="lg" aria-hidden="true"><use href="#i-github" /></svg>GitHub</span>
        <span aria-hidden="true"><svg className="lg" aria-hidden="true"><use href="#i-vercel" /></svg>Vercel</span><span aria-hidden="true"><svg className="lg" aria-hidden="true"><use href="#i-gcp" /></svg>Google Cloud</span><span aria-hidden="true"><svg className="lg" aria-hidden="true"><use href="#i-aws" /></svg>AWS</span><span aria-hidden="true"><svg className="lg" aria-hidden="true"><use href="#i-render" /></svg>Render</span><span aria-hidden="true"><svg className="lg" aria-hidden="true"><use href="#i-digitalocean" /></svg>DigitalOcean</span><span aria-hidden="true"><i></i>Azure</span><span aria-hidden="true"><svg className="lg" aria-hidden="true"><use href="#i-docker" /></svg>Docker</span><span aria-hidden="true"><svg className="lg" aria-hidden="true"><use href="#i-kubernetes" /></svg>Kubernetes</span><span aria-hidden="true"><svg className="lg" aria-hidden="true"><use href="#i-postgresql" /></svg>PostgreSQL</span><span aria-hidden="true"><svg className="lg" aria-hidden="true"><use href="#i-mysql" /></svg>MySQL</span><span aria-hidden="true"><svg className="lg" aria-hidden="true"><use href="#i-mariadb" /></svg>MariaDB</span><span aria-hidden="true"><svg className="lg" aria-hidden="true"><use href="#i-redis" /></svg>Redis</span><span aria-hidden="true"><svg className="lg" aria-hidden="true"><use href="#i-github" /></svg>GitHub</span>
      </div>
    </div>

    <div className="wrap">
      <div className="stats">
        <div className="stat"><b>6<em>+</em></b><span>clouds for services, plus your own servers and clusters</span></div>
        <div className="stat"><b>6</b><span>language ecosystems read without running a line</span></div>
        <div className="stat"><b>5</b><span>database and cache engines you can deploy in one click</span></div>
        <div className="stat"><b>30<em>m</em></b><span>between drift checks by default, so live matches deployed</span></div>
      </div>
    </div>

    <section className="band paper" aria-labelledby="how-title">
      <div className="wrap sec">
        <div className="eyebrow">How it works</div>
        <h2 id="how-title">From repository to running services, one reviewed plan at a time.</h2>
        <p className="sec-lede">Every deployment goes through the same six steps. Nothing is provisioned until someone with the Admin role approves the exact plan you reviewed.</p>
        <HowItWorks />
      </div>
    </section>

    <section className="band" aria-labelledby="explore-title">
      <div className="wrap sec">
        <div className="sec-head">
          <div>
            <div className="eyebrow">Everything in the box</div>
            <h2 id="explore-title">One platform for the whole path to production.</h2>
          </div>
          <p className="sec-lede" style={{ maxWidth: "44ch" } as CSSProperties}>Loomplane is one Go process and a worker on one PostgreSQL database. Here is what it does once it's running.</p>
        </div>
        <div className="grid">
          <Link className="cell" href="/platform"><span className="tag">Platform</span><h4>Discovery, manifests and plans</h4><p>Static analysis of your repositories, a versioned <code>loomplane.yaml</code>, and plans that list every action before anything moves.</p><span className="more">Explore the platform <span className="arr">↗</span></span></Link>
          <Link className="cell" href="/clouds"><span className="tag">Clouds</span><h4>Six clouds and your own hardware</h4><p>Vercel, Cloud Run, ECS, Render, App Platform and Container Apps, plus SSH servers and Kubernetes. Pick targets by tag and move between them.</p><span className="more">See every target <span className="arr">↗</span></span></Link>
          <Link className="cell" href="/services"><span className="tag">Services</span><h4>Databases with tested recovery</h4><p>PostgreSQL, MySQL, MariaDB, Redis and Valkey in one click, plus the Octopus gateway and the Fabriq data fabric, with scheduled backups and restores you can test on an isolated copy.</p><span className="more">Browse the catalog <span className="arr">↗</span></span></Link>
          <Link className="cell" href="/agents"><span className="tag">CLI and agents</span><h4>loomctl and an MCP server</h4><p>Everything the dashboard does, from a terminal or CI. Give an agent tools to analyze and plan, and approve only if you allow it.</p><span className="more">Read the CLI tour <span className="arr">↗</span></span></Link>
          <Link className="cell" href="/security"><span className="tag">Security</span><h4>Roles, secrets and an audit trail</h4><p>Organization isolation on every request, Warden roles on every action, and secrets that live in Vault and each cloud's own store.</p><span className="more">How it's protected <span className="arr">↗</span></span></Link>
          <Link className="cell" href="/self-host"><span className="tag">Self-host</span><h4>Run it on a laptop or in production</h4><p>Docker Compose for local work, a production guide with HTTPS and backups, live state over WebSocket, and Prometheus metrics.</p><span className="more">Install Loomplane <span className="arr">↗</span></span></Link>
        </div>
        <div className="langs">
          <div className="lang"><svg className="lg" style={{ color: "#00ADD8" } as CSSProperties} aria-hidden="true"><use href="#i-go" /></svg><b>Go</b><span>main packages</span></div>
          <div className="lang"><svg className="lg" style={{ color: "var(--ink)" } as CSSProperties} aria-hidden="true"><use href="#i-rust" /></svg><b>Rust</b><span>binary sources</span></div>
          <div className="lang"><svg className="lg" style={{ color: "#4A8FDB" } as CSSProperties} aria-hidden="true"><use href="#i-typescript" /></svg><b>TypeScript</b><span>Next.js, Node</span></div>
          <div className="lang"><svg className="lg" style={{ color: "#F7DF1E" } as CSSProperties} aria-hidden="true"><use href="#i-javascript" /></svg><b>JavaScript</b><span>packages</span></div>
          <div className="lang"><svg className="lg" style={{ color: "#5A9FD4" } as CSSProperties} aria-hidden="true"><use href="#i-python" /></svg><b>Python</b><span>Procfile processes</span></div>
          <div className="lang"><svg className="lg" style={{ color: "var(--ink)" } as CSSProperties} aria-hidden="true"><use href="#i-openjdk" /></svg><b>JVM</b><span>Maven, Gradle</span></div>
        </div>
      </div>
    </section>

    <section className="band" aria-labelledby="secrets-home">
      <div className="wrap">
        <div className="feature">
          <div className="txt">
            <div className="eyebrow">Variables and secrets</div>
            <h3 className="h3" id="secrets-home">Set a value once. Override it exactly where it differs.</h3>
            <p>Variables come in layers: organization, project, service, and each environment. Secrets go to Vault first and then to Secret Manager, AWS Secrets Manager or Key Vault. After you save one, nobody sees it again, including you.</p>
            <ul className="ticks">
              <li>Services find each other through variables Loomplane writes for you</li>
              <li>Generated database passwords never leave Vault and the target cloud</li>
              <li>Viewers read everything except secret values</li>
            </ul>
          </div>
          <div className="vis vis-stack"><span className="ide-cross vis-cross a" aria-hidden="true"></span><span className="ide-cross vis-cross b" aria-hidden="true"></span><div className="vis-ghost ide-bezel" aria-hidden="true"><div className="ide"><div className="ghost-h"></div><div className="ghost-lines"><i></i><i></i><i></i><i></i><i></i></div></div></div><div className="ide-bezel vis-main"><div className="panel">
            <div className="panel-h"><b>api · production</b><small>resolved variables</small></div>
            <div className="rows">
              <div className="row"><div className="l"><span className="dot wait"></span><div><b>LOG_LEVEL</b><small>organization · info, overridden</small></div></div><span className="r2">warn</span></div>
              <div className="row"><div className="l"><span className="dot blue"></span><div><b>SENTRY_DSN</b><small>project · secret</small></div></div><span className="r2">••••••••</span></div>
              <div className="row"><div className="l"><span className="dot blue"></span><div><b>DATABASE_URL</b><small>generated · from db</small></div></div><span className="r2">••••••••</span></div>
              <div className="row"><div className="l"><span className="dot blue"></span><div><b>REDIS_URL</b><small>generated · from cache</small></div></div><span className="r2">••••••••</span></div>
              <div className="row"><div className="l"><span className="dot ok"></span><div><b>PORT</b><small>service</small></div></div><span className="r2">8080</span></div>
              <div className="row"><div className="l"><span className="dot ok"></span><div><b>WEB_URL</b><small>generated · from web</small></div></div><span className="r2">https://acme.dev</span></div>
            </div>
          </div></div></div>
        </div>
      </div>
    </section>

    <section className="band paper" aria-labelledby="faq-title">
      <div className="wrap sec">
        <div className="eyebrow">Questions</div>
        <h2 id="faq-title">What people ask first.</h2>
        <div className="faq">
          <details><summary>Do my applications need Forge or Go?</summary><p>No. Loomplane itself is built on Go and the Forge ecosystem, but it deploys Go, Rust, TypeScript, JavaScript, Python and JVM applications. It reads your code to understand it and builds each service on its cloud's own builder.</p></details>
          <details><summary>Does Loomplane run my code during analysis?</summary><p>Never. Analysis is static. It reads package manifests, Dockerfiles, Compose files and source layout, and every service it finds links to the line that gave it away. Examples, fixtures, test data and scripts are left out of the default graph.</p></details>
          <details><summary>Where do my workloads run?</summary><p>In your own cloud accounts. Loomplane holds credentials for the clouds you connect, plans the changes and drives each provider's API. Builds run on Cloud Build, CodeBuild, Vercel, Render, App Platform and ACR Tasks, in your accounts.</p></details>
          <details><summary>What happens if someone changes something in the console?</summary><p>A drift check runs every 30 minutes by default. It compares what's deployed with what's live, and the next plan puts it back. You review that plan like any other before it runs.</p></details>
          <details><summary>Can I deploy to my own servers?</summary><p>Yes. Connect an SSH server with Docker or a Kubernetes namespace. For anything else, write a provider plugin and point Loomplane at it without changing Loomplane itself.</p></details>
          <details><summary>Can an AI agent deploy for me?</summary><p><code>loomctl mcp</code> gives an agent tools to analyze, draft manifests, plan and follow deployments. Approving a plan or retrying a deployment needs <code>--allow-deploy</code>, so it only happens if you let it.</p></details>
        </div>
      </div>
    </section>
  
    </>
  );
}
