import type { CSSProperties } from 'react';
import type { Metadata } from 'next';
import Link from 'next/link';
import { ServicesRoadmap } from '@/components/landing/services-roadmap';

export const metadata: Metadata = {
  title: "Services",
  description: "Databases, the Octopus gateway and the Fabriq data fabric in one click, with scheduled backups and restores you can test.",
};

export default function ServicesPage() {
  return (
    <>

    <section className="page-hero grain" aria-labelledby="services-title">
      <div className="wrap">
        <div className="crumb rise"><Link href="/">Loomplane</Link> / <b>Services</b></div>
        <h1 id="services-title">Databases you can <span className="thread">actually restore.</span></h1>
        <p className="lede">Deploy a database without a repository. Pick an engine, choose where it runs, and Loomplane creates a project, checks the plan and approves only the one creation you asked for. Then it backs it up on a schedule and proves the backups restore.</p>
        <div className="engines">
          <div className="engine"><span className="glyph"><svg className="lg" style={{ color: "#6A8DF0" } as CSSProperties} aria-hidden="true"><use href="#i-postgresql" /></svg></span><b>PostgreSQL</b><small>17 · Docker, Kubernetes, RDS, Cloud SQL</small><span className="deploy">Deploy PostgreSQL ↗</span></div>
          <div className="engine"><span className="glyph"><svg className="lg" style={{ color: "#5E9CCB" } as CSSProperties} aria-hidden="true"><use href="#i-mysql" /></svg></span><b>MySQL</b><small>8.4 · native backup client</small><span className="deploy">Deploy MySQL ↗</span></div>
          <div className="engine"><span className="glyph"><svg className="lg" style={{ color: "var(--ink)" } as CSSProperties} aria-hidden="true"><use href="#i-mariadb" /></svg></span><b>MariaDB</b><small>11.8 · native backup client</small><span className="deploy">Deploy MariaDB ↗</span></div>
          <div className="engine"><span className="glyph"><svg className="lg" style={{ color: "#FF4438" } as CSSProperties} aria-hidden="true"><use href="#i-redis" /></svg></span><b>Redis</b><small>8.2 · on a checked Docker server</small><span className="deploy">Deploy Redis ↗</span></div>
          <div className="engine"><span className="glyph">Vk</span><b>Valkey</b><small>8.1 · on a checked Docker server</small><span className="deploy">Deploy Valkey ↗</span></div>
        </div>
      </div>
    </section>

    <ServicesRoadmap />

    <div className="wrap">
      <div className="feature">
        <div className="txt">
          <div className="eyebrow">Recovery</div>
          <h3 className="h3">A backup isn't real until it's been restored.</h3>
          <p><b style={{ fontWeight: "500", color: "var(--ink)" } as CSSProperties}>Back up now</b> queues an export with the engine's own backup client. Set a schedule from every hour to once a week. Each recovery point starts as <em>Not tested</em>. <b style={{ fontWeight: "500", color: "var(--ink)" } as CSSProperties}>Test restore</b> builds an isolated copy with its own volume and credentials, restores into it, and checks that an application can connect.</p>
          <ul className="ticks">
            <li>Test copies have no published ports and expire after 24 hours</li>
            <li>PostgreSQL restores run in one transaction</li>
            <li>Backup history stays even if you remove the source service</li>
            <li>Retention policies you turn on yourself, with a preview first</li>
          </ul>
        </div>
        <div className="vis vis-inspect"><span className="ide-cross vis-cross a" aria-hidden="true"></span><span className="ide-cross vis-cross b" aria-hidden="true"></span><div className="ide-bezel vis-main"><div className="vis-bar"><span className="dots"><i></i><i></i><i></i></span><span className="vb-crumb">services / recovery</span><span className="chip ok">scheduled</span></div><div className="panel">
          <div className="panel-h"><b>orders-db · production</b><small>every 6 hours</small></div>
          <div className="rows">
            <div className="row"><div className="l"><span className="dot ok"></span><div><b>Oct 22, 12:00</b><small>pg_dump · 1.8 GB · verified in 2m 14s</small></div></div><span className="chip ok">verified</span></div>
            <div className="row"><div className="l"><span className="dot run"></span><div><b>Oct 22, 06:00</b><small>test copy restoring · expires in 23h</small></div></div><span className="chip warn">testing</span></div>
            <div className="row"><div className="l"><span className="dot wait"></span><div><b>Oct 22, 00:00</b><small>pg_dump · 1.8 GB</small></div></div><span className="chip">not tested</span></div>
            <div className="row"><div className="l"><span className="dot ok"></span><div><b>Oct 21, 18:00</b><small>pg_dump · 1.7 GB · verified in 2m 09s</small></div></div><span className="chip ok">verified</span></div>
          </div>
        </div><div className="vis-foot"><span className="live" aria-hidden="true"></span>3 verified · 1 testing · next backup 18:00</div></div></div>
      </div>

      <div className="feature flip">
        <div className="txt">
          <div className="eyebrow">Managed and native</div>
          <h3 className="h3">Your server, or the cloud's managed database.</h3>
          <p>Run a digest-pinned image on your own Docker server or Kubernetes cluster, with retained storage and a separate application role that can't create databases or roles. Or create PostgreSQL 17 on RDS or Cloud SQL, review the compute, disk, access and backup settings, and approve only that.</p>
          <ul className="ticks">
            <li>Removing an RDS or Cloud SQL database takes a final snapshot first</li>
            <li>Lost responses and restarts resume from a saved checkpoint</li>
            <li>Provider-native snapshot history alongside Loomplane's exports</li>
          </ul>
        </div>
        <div className="vis vis-glow"><span className="ide-cross vis-cross a" aria-hidden="true"></span><span className="ide-cross vis-cross b" aria-hidden="true"></span><div className="ide-bezel vis-main"><div className="panel">
          <div className="panel-h"><b>New PostgreSQL</b><small>review before creating</small></div>
          <div className="rows">
            <div className="row"><div className="l"><div><b>Account</b><small>aws · acme-prod · eu-west-1</small></div></div><span className="r2">checked</span></div>
            <div className="row"><div className="l"><div><b>Instance</b><small>db.t4g.medium · 2 vCPU · 4 GiB</small></div></div><span className="r2">RDS</span></div>
            <div className="row"><div className="l"><div><b>Storage</b><small>100 GiB gp3 · encrypted</small></div></div><span className="r2">retained</span></div>
            <div className="row"><div className="l"><div><b>Access</b><small>private · project services only</small></div></div><span className="r2">no public IP</span></div>
            <div className="row"><div className="l"><div><b>Backups</b><small>automated · 7 days</small></div></div><span className="r2">on</span></div>
          </div>
        </div></div></div>
      </div>
    </div>
  
    </>
  );
}
