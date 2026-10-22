import type { Codec } from '../codec-runtime';
import { codec_EditInputBody_add_resource_uses } from './EditInputBody.add_resource.uses';

export const codec_EditInputBody_add_resource: Codec = {"kind": "object", "fields": {"kind": {"ts": "kind"}, "name": {"ts": "name"}, "provider": {"ts": "provider"}, "target": {"ts": "target"}, "uses": {"ts": "uses", "codec": () => codec_EditInputBody_add_resource_uses}, "version": {"ts": "version"}}, "required": ["kind", "name"]};
