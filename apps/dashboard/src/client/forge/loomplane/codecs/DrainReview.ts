import type { Codec } from '../codec-runtime';

export const codec_DrainReview: Codec = {"kind": "object", "fields": {"reservations": {"ts": "reservations"}, "review": {"ts": "review"}, "server_id": {"ts": "serverId"}, "server_revision": {"ts": "serverRevision"}}, "required": ["reservations", "review", "server_id", "server_revision"]};
