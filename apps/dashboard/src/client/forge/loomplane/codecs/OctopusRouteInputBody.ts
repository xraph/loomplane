import type { Codec } from '../codec-runtime';
import { codec_OctopusRouteInputBody_methods } from './OctopusRouteInputBody.methods';

export const codec_OctopusRouteInputBody: Codec = {"kind": "object", "fields": {"hostname": {"ts": "hostname"}, "methods": {"ts": "methods", "codec": () => codec_OctopusRouteInputBody_methods}, "note": {"ts": "note"}, "path": {"ts": "path"}, "port": {"ts": "port"}, "revision": {"ts": "revision"}, "service": {"ts": "service"}, "strip_prefix": {"ts": "stripPrefix"}}, "required": ["hostname", "methods", "path", "port", "service", "strip_prefix"]};
