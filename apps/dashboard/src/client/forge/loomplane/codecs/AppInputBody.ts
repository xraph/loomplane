import type { Codec } from '../codec-runtime';

export const codec_AppInputBody: Codec = {"kind": "object", "fields": {"client_id": {"ts": "clientId"}, "client_secret": {"ts": "clientSecret"}, "slug": {"ts": "slug"}}, "required": ["client_id", "client_secret"]};
