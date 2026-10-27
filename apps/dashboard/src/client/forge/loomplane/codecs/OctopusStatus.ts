import type { Codec } from '../codec-runtime';
import { codec_AttachDefaults } from './AttachDefaults';
import { codec_OctopusStatus_farp_hostnames } from './OctopusStatus.farp_hostnames';

export const codec_OctopusStatus: Codec = {"kind": "object", "fields": {"applied_generation": {"ts": "appliedGeneration"}, "attach": {"ts": "attach", "codec": () => codec_AttachDefaults}, "farp_hostnames": {"ts": "farpHostnames", "codec": () => codec_OctopusStatus_farp_hostnames}, "generation": {"ts": "generation"}, "namespace": {"ts": "namespace"}, "private_url": {"ts": "privateUrl"}, "provider": {"ts": "provider"}, "resource": {"ts": "resource"}, "revision": {"ts": "revision"}, "start_time": {"ts": "startTime"}, "sync_error": {"ts": "syncError"}, "synced_at": {"ts": "syncedAt"}, "version": {"ts": "version"}}, "required": ["applied_generation", "attach", "farp_hostnames", "generation", "provider", "resource", "revision", "synced_at"]};
