import type { Codec } from '../codec-runtime';
import { codec_OctopusloomRoute } from './OctopusloomRoute';
import { codec_SyncResult } from './SyncResult';

export const codec_RouteResult: Codec = {"kind": "object", "fields": {"route": {"ts": "route", "codec": () => codec_OctopusloomRoute}, "sync": {"ts": "sync", "codec": () => codec_SyncResult}}, "required": ["sync"]};
