import type { Codec } from '../codec-runtime';
import { codec_EditInputBody_update_resource_settings } from './EditInputBody.update_resource.settings';
import { codec_ManifestGPU } from './ManifestGPU';

export const codec_EditInputBody_update_resource: Codec = {"kind": "object", "fields": {"clear_gpu": {"ts": "clearGpu"}, "enabled": {"ts": "enabled"}, "environment": {"ts": "environment"}, "gpu": {"ts": "gpu", "codec": () => codec_ManifestGPU}, "provider": {"ts": "provider"}, "resource": {"ts": "resource"}, "settings": {"ts": "settings", "codec": () => codec_EditInputBody_update_resource_settings}, "target": {"ts": "target"}}, "required": ["resource"]};
