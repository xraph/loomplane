import type { Codec } from '../codec-runtime';

export const codec_RouteVerification: Codec = {"kind": "object", "fields": {"backend": {"ts": "backend"}, "backend_error": {"ts": "backendError"}, "cert_expires": {"ts": "certExpires"}, "cert_state": {"ts": "certState"}, "checked_at": {"ts": "checkedAt"}, "dns": {"ts": "dns"}, "dns_error": {"ts": "dnsError"}, "tls": {"ts": "tls"}, "tls_error": {"ts": "tlsError"}}, "required": ["backend", "cert_expires", "checked_at", "dns", "tls"]};
