import type { Codec } from '../codec-runtime';

export const codec_NewKeyBody: Codec = {"kind": "object", "fields": {"access": {"ts": "access"}, "expires_in_days": {"ts": "expiresInDays"}, "name": {"ts": "name"}}, "required": ["access", "name"]};
