import type { Codec } from '../codec-runtime';
import { codec_CapacityPolicy } from './CapacityPolicy';
import { codec_PoolExpansionPolicy } from './PoolExpansionPolicy';

export const codec_ScalingPoolInputBody: Codec = {"kind": "object", "fields": {"accelerator_kind": {"ts": "acceleratorKind"}, "expected_revision": {"ts": "expectedRevision"}, "name": {"ts": "name"}, "policy": {"ts": "policy", "codec": () => codec_CapacityPolicy}, "template": {"ts": "template", "codec": () => codec_PoolExpansionPolicy}}, "required": ["expected_revision", "name", "policy", "template"]};
