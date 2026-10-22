import type { Codec } from '../codec-runtime';
import { codec_ServerUsage } from './ServerUsage';

export const codec_DrainReview: Codec = {"kind": "object", "fields": {"reservations": {"ts": "reservations"}, "review": {"ts": "review"}, "server_id": {"ts": "serverId"}, "server_revision": {"ts": "serverRevision"}, "usage": {"ts": "usage", "codec": () => codec_ServerUsage}}, "required": ["reservations", "review", "server_id", "server_revision"]};
