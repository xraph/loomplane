import type { Codec } from '../codec-runtime';

export const codec_AddGitLabHostInputBody: Codec = {"kind": "object", "fields": {"client_id": {"ts": "clientId"}, "client_secret": {"ts": "clientSecret"}, "web_url": {"ts": "webUrl"}}, "required": ["client_id", "client_secret", "web_url"]};
