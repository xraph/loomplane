import { Archive, Copy, Layers, RotateCcw, ShieldCheck, Waypoints } from 'lucide-react';
import type { ReactNode } from 'react';

/*
 * What's on the Services page beyond the five database engines: the two Xraph
 * platform services in the catalog, the engines on the way, and data
 * protection with each feature marked available or coming soon.
 */

const platform = [
  {
    name: 'Octopus',
    icon: <Waypoints />,
    kind: 'API gateway',
    body: 'A gateway written in Rust that routes HTTP traffic to your services. Services that speak FARP publish their own OpenAPI, AsyncAPI, gRPC or GraphQL schemas, and Octopus turns them into routes as they come and go.',
    points: [
      'HTTP/2, WebSockets, load balancing, health checks and circuit breaking',
      'JWT auth, rate limiting, CORS and compression as middleware',
      'Your services get its address as OCTOPUS_URL',
    ],
  },
  {
    name: 'Fabriq',
    icon: <Layers />,
    kind: 'Data fabric',
    body: 'A multi-tenant data fabric for Go. Every write commits once in Postgres with a versioned event, and a relay projects it to a FalkorDB graph, an Elasticsearch index, caches and live subscribers.',
    points: [
      'Typed reads for relational, time-series, vector, spatial, graph and search',
      'Every call scoped to a tenant with Postgres row-level security',
      'Link it from a service with needs: [fabriq] and a tenant option',
    ],
  },
];

const engines = ['Memcached', 'FalkorDB', 'Elasticsearch', 'TimescaleDB', 'MongoDB', 'ClickHouse', 'Kafka', 'NATS', 'MinIO'];

const protection: { icon: ReactNode; title: string; body: string; soon?: boolean }[] = [
  {
    icon: <Archive />,
    title: 'One-click backups',
    body: 'Back up now, or set a schedule from every hour to once a week. Each export uses the engine’s own backup client.',
  },
  {
    icon: <RotateCcw />,
    title: 'Restores you can test',
    body: 'Restore any recovery point into an isolated copy and check that an application can connect, without touching the live database.',
  },
  {
    icon: <ShieldCheck />,
    title: 'Retention you control',
    body: 'Recovery points stay until you say otherwise. Turn on a retention policy and preview what it would remove first.',
  },
  {
    icon: <Copy />,
    title: 'Service replication',
    body: 'Keep a second copy of a database in step on another server or zone, ready to take over.',
    soon: true,
  },
];

function Soon() {
  return <span className="chip warn">Coming soon</span>;
}

function Available() {
  return <span className="chip ok">Available</span>;
}

export function ServicesRoadmap() {
  return (
    <>
      <section className="band" aria-labelledby="platform-services">
        <div className="wrap sec">
          <div className="sec-head">
            <div>
              <div className="eyebrow">Platform services</div>
              <h2 id="platform-services">Octopus and Fabriq, one click away.</h2>
            </div>
            <p className="sec-lede" style={{ maxWidth: '46ch' }}>
              Two Xraph services are in the catalog. Deploy them next to your apps like any database, and link your
              services to them from the manifest.
            </p>
          </div>
          <div className="psvc">
            {platform.map((p) => (
              <article className="psvc-card" key={p.name}>
                <div className="psvc-top">
                  <span className="glyph">{p.icon}</span>
                  <div>
                    <b>{p.name}</b>
                    <small>{p.kind}</small>
                  </div>
                  <Available />
                </div>
                <p>{p.body}</p>
                <ul className="ticks">
                  {p.points.map((t) => (
                    <li key={t}>{t}</li>
                  ))}
                </ul>
              </article>
            ))}
          </div>

          <div className="soon-engines">
            <span className="soon-label">More engines on the way</span>
            <ul>
              {engines.map((e) => (
                <li key={e}>{e}</li>
              ))}
            </ul>
          </div>
        </div>
      </section>

      <section className="band" aria-labelledby="data-protection">
        <div className="wrap sec">
          <div className="eyebrow">Data protection</div>
          <h2 id="data-protection">Backups that restore, and copies that keep up.</h2>
          <div className="grid four">
            {protection.map((f) => (
              <div className="cell" key={f.title}>
                <div className="prot-top">
                  <span className="prot-ico">{f.icon}</span>
                  {f.soon ? <Soon /> : <Available />}
                </div>
                <h4>{f.title}</h4>
                <p>{f.body}</p>
              </div>
            ))}
          </div>
        </div>
      </section>
    </>
  );
}
