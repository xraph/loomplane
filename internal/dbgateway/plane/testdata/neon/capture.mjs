// Drives @neondatabase/serverless against a recording endpoint so
// neon_capture_test.go can see exactly what the driver sends.
//
// CAPTURE_URL is the ws:// base of the recorder. DSN is the connection string
// the app would hold. The recorder never answers the Postgres protocol, so
// both calls are expected to fail or time out. We only care about the bytes
// that arrive before then.
import { Pool, neon, neonConfig } from '@neondatabase/serverless';
import ws from 'ws';

const captureUrl = process.env.CAPTURE_URL;
const dsn = process.env.DSN;
if (!captureUrl || !dsn) {
  console.error('CAPTURE_URL and DSN are required');
  process.exit(2);
}

neonConfig.webSocketConstructor = ws;
neonConfig.useSecureWebSocket = false;
// The driver prepends ws:// itself, so the proxy value must not carry a scheme.
neonConfig.wsProxy = (host, port) => `${captureUrl.replace(/^ws:\/\//, "")}/v2?address=${host}:${port}`;
neonConfig.fetchEndpoint = () => `${captureUrl.replace('ws', 'http')}/sql`;

const withTimeout = (p, ms) =>
  Promise.race([p, new Promise((resolve) => setTimeout(resolve, ms))]);

const pool = new Pool({ connectionString: dsn });
pool.on('error', () => {});
try {
  await withTimeout(pool.query('SELECT 1'), 3000);
} catch {
  // Expected: the recorder does not speak Postgres.
}

const sql = neon(dsn);
try {
  await withTimeout(sql.query('SELECT $1::int AS n', [7]), 3000);
} catch {
  // Expected: the recorder answers with an empty body.
}

process.exit(0);
