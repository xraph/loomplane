import type { Codec } from '../codec-runtime';
import { codec_Allotment } from './Allotment';
import { codec_PlatformcloudEvidence } from './PlatformcloudEvidence';

export const codec_Host: Codec = {"kind": "object", "fields": {"allotment": {"ts": "allotment", "codec": () => codec_Allotment}, "client_fingerprint": {"ts": "clientFingerprint"}, "endpoint": {"ts": "endpoint"}, "evidence": {"ts": "evidence", "codec": () => codec_PlatformcloudEvidence}, "generation": {"ts": "generation"}, "host_id": {"ts": "hostId"}, "id": {"ts": "id"}, "region_id": {"ts": "regionId"}, "revision": {"ts": "revision"}, "status": {"ts": "status"}}, "required": ["allotment", "client_fingerprint", "endpoint", "evidence", "generation", "host_id", "id", "region_id", "revision", "status"]};
