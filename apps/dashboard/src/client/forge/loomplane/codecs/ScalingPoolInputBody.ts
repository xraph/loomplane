import type { Codec } from '../codec-runtime';
import { codec_ScalingPoolInputBody_policy } from './ScalingPoolInputBody.policy';
import { codec_ScalingPoolInputBody_template } from './ScalingPoolInputBody.template';

export const codec_ScalingPoolInputBody: Codec = {"kind": "object", "fields": {"accelerator_kind": {"ts": "acceleratorKind"}, "expected_revision": {"ts": "expectedRevision"}, "name": {"ts": "name"}, "policy": {"ts": "policy", "codec": () => codec_ScalingPoolInputBody_policy}, "template": {"ts": "template", "codec": () => codec_ScalingPoolInputBody_template}}, "required": ["expected_revision", "name", "policy", "template"]};
