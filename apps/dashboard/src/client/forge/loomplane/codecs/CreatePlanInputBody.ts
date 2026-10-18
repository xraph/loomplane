import type { Codec } from '../codec-runtime';
import { codec_CreatePlanInputBody_scope } from './CreatePlanInputBody.scope';

export const codec_CreatePlanInputBody: Codec = {"kind": "object", "fields": {"catalog": {"ts": "catalog"}, "environment": {"ts": "environment"}, "scope": {"ts": "scope", "codec": () => codec_CreatePlanInputBody_scope}}, "required": ["environment"]};
