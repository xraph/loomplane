import type { Codec } from '../codec-runtime';
import { codec_EditInputBody_add_resource_attachments } from './EditInputBody.add_resource.attachments';
import { codec_EditInputBody_add_resource_consumer_options } from './EditInputBody.add_resource.consumer_options';
import { codec_EditInputBody_add_resource_needed_by } from './EditInputBody.add_resource.needed_by';
import { codec_EditInputBody_add_resource_settings } from './EditInputBody.add_resource.settings';
import { codec_EditInputBody_add_resource_uses } from './EditInputBody.add_resource.uses';
import { codec_EditInputBody_add_resource_wiring } from './EditInputBody.add_resource.wiring';
import { codec_ManifestGPU } from './ManifestGPU';

export const codec_EditInputBody_add_resource: Codec = {"kind": "object", "fields": {"attachments": {"ts": "attachments", "codec": () => codec_EditInputBody_add_resource_attachments}, "consumer_options": {"ts": "consumerOptions", "codec": () => codec_EditInputBody_add_resource_consumer_options}, "enabled_in": {"ts": "enabledIn"}, "existing_resource": {"ts": "existingResource"}, "gpu": {"ts": "gpu", "codec": () => codec_ManifestGPU}, "kind": {"ts": "kind"}, "name": {"ts": "name"}, "needed_by": {"ts": "neededBy", "codec": () => codec_EditInputBody_add_resource_needed_by}, "only": {"ts": "only"}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "settings": {"ts": "settings", "codec": () => codec_EditInputBody_add_resource_settings}, "target": {"ts": "target"}, "uses": {"ts": "uses", "codec": () => codec_EditInputBody_add_resource_uses}, "version": {"ts": "version"}, "wiring": {"ts": "wiring", "codec": () => codec_EditInputBody_add_resource_wiring}}, "required": ["kind", "name"]};
