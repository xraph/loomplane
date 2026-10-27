// Drives @neondatabase/serverless against a running database gateway for
// extensions/dbgateway's TestE2ENeonDriver, configured the way
// docs/postgres-services.md tells a Vercel app to.
//
// GATEWAY is the gateway's host:port, served over plain ws:// and http://.
// DSN is the app's DATABASE_URL, with the gateway's logical host. BAD_DSN is
// the same with a wrong password. It prints one JSON object with what each
// call returned or the SQLSTATE it failed with.
import { Pool, neon, neonConfig } from '@neondatabase/serverless';
import ws from 'ws';

const { GATEWAY: gateway, DSN: dsn, BAD_DSN: badDsn } = process.env;
if (!gateway || !dsn || !badDsn) {
  console.error('GATEWAY, DSN and BAD_DSN are required');
  process.exit(2);
}

neonConfig.webSocketConstructor = ws;
neonConfig.wsProxy = (host, port) => `${gateway}/v2?address=${host}:${port}`;
neonConfig.fetchEndpoint = () => `http://${gateway}/sql`;
// The test gateway listens on plain ws://; a deployed one is behind https.
neonConfig.useSecureWebSocket = false;

const failure = (err) => ({ code: err?.code ?? null, message: String(err?.message ?? err) });

const out = {};

const pool = new Pool({ connectionString: dsn });
try {
  const { rows } = await pool.query('SELECT 1 AS one');
  out.ws = { one: rows[0].one };
} catch (err) {
  out.ws = failure(err);
}
await pool.end();

try {
  const rows = await neon(dsn).query('SELECT $1::int AS n', [7]);
  out.http = { n: rows[0].n };
} catch (err) {
  out.http = failure(err);
}

const bad = new Pool({ connectionString: badDsn });
bad.on('error', () => {});
try {
  await bad.query('SELECT 1');
  out.wsBadPassword = { accepted: true };
} catch (err) {
  out.wsBadPassword = failure(err);
}
await bad.end().catch(() => {});

try {
  await neon(badDsn).query('SELECT 1');
  out.httpBadPassword = { accepted: true };
} catch (err) {
  out.httpBadPassword = failure(err);
}

console.log(JSON.stringify(out));
process.exit(0);
