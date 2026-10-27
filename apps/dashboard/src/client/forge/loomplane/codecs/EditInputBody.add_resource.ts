import type { Codec } from '../codec-runtime';
import { codec_EditInputBody_add_resource_settings } from './EditInputBody.add_resource.settings';
import { codec_EditInputBody_add_resource_uses } from './EditInputBody.add_resource.uses';
import { codec_ManifestGPU } from './ManifestGPU';

export const codec_EditInputBody_add_resource: Codec = {"kind": "object", "fields": {"enabled_in": {"ts": "enabledIn"}, "gpu": {"ts": "gpu", "codec": () => codec_ManifestGPU}, "kind": {"ts": "kind"}, "name": {"ts": "name"}, "provider": {"ts": "provider"}, "settings": {"ts": "settings", "codec": () => codec_EditInputBody_add_resource_settings}, "target": {"ts": "target"}, "uses": {"ts": "uses", "codec": () => codec_EditInputBody_add_resource_uses}, "version": {"ts": "version"}}, "required": ["kind", "name"]};
