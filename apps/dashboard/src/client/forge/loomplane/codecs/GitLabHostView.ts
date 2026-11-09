import type { Codec } from '../codec-runtime';

export const codec_GitLabHostView: Codec = {"kind": "object", "fields": {"client_id": {"ts": "clientId"}, "created_at": {"ts": "createdAt"}, "host": {"ts": "host"}, "id": {"ts": "id"}, "instance": {"ts": "instance"}, "web_url": {"ts": "webUrl"}}, "required": ["client_id", "host", "id", "instance", "web_url"]};
