import type { Codec } from '../codec-runtime';

export const codec_CertificateView: Codec = {"kind": "object", "fields": {"days_left": {"ts": "daysLeft"}, "expires_at": {"ts": "expiresAt"}, "rotation": {"ts": "rotation"}, "warning": {"ts": "warning"}}, "required": ["rotation", "warning"]};
