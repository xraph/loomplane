import type { Codec } from '../codec-runtime';
import { codec_HostInputBody_allotment } from './HostInputBody.allotment';

export const codec_HostInputBody: Codec = {"kind": "object", "fields": {"allotment": {"ts": "allotment", "codec": () => codec_HostInputBody_allotment}, "endpoint": {"ts": "endpoint"}, "host_id": {"ts": "hostId"}, "region_id": {"ts": "regionId"}, "server_ca": {"ts": "serverCa"}}, "required": ["allotment", "endpoint", "host_id", "region_id", "server_ca"]};
