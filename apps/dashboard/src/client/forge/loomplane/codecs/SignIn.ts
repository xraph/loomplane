import type { Codec } from '../codec-runtime';

export const codec_SignIn: Codec = {"kind": "object", "fields": {"authorize_url": {"ts": "authorizeUrl"}, "state": {"ts": "state"}}, "required": ["authorize_url", "state"]};
