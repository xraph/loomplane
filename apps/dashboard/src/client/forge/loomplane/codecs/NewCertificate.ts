import type { Codec } from '../codec-runtime';
import { codec_Host } from './Host';

export const codec_NewCertificate: Codec = {"kind": "object", "fields": {"client_certificate": {"ts": "clientCertificate"}, "client_fingerprint": {"ts": "clientFingerprint"}, "expires_at": {"ts": "expiresAt"}, "host": {"ts": "host", "codec": () => codec_Host}}, "required": ["client_certificate", "client_fingerprint", "expires_at", "host"]};
