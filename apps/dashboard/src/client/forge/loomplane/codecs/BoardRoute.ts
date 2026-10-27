import type { Codec } from '../codec-runtime';
import { codec_BoardRoute_methods } from './BoardRoute.methods';

export const codec_BoardRoute: Codec = {"kind": "object", "fields": {"key": {"ts": "key"}, "methods": {"ts": "methods", "codec": () => codec_BoardRoute_methods}, "note": {"ts": "note"}, "overrides": {"ts": "overrides"}, "path": {"ts": "path"}, "port": {"ts": "port"}, "revision": {"ts": "revision"}, "route_id": {"ts": "routeId"}, "service": {"ts": "service"}, "source": {"ts": "source"}, "strip_prefix": {"ts": "stripPrefix"}, "sync": {"ts": "sync"}, "sync_error": {"ts": "syncError"}}, "required": ["key", "methods", "note", "overrides", "path", "port", "service", "source", "strip_prefix"]};
