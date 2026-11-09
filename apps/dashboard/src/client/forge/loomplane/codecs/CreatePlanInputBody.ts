import type { Codec } from '../codec-runtime';
import { codec_CreatePlanInputBody_scope } from './CreatePlanInputBody.scope';
import { codec_CreatePlanInputBody_services } from './CreatePlanInputBody.services';

export const codec_CreatePlanInputBody: Codec = {"kind": "object", "fields": {"catalog": {"ts": "catalog"}, "environment": {"ts": "environment"}, "from": {"ts": "from"}, "scope": {"ts": "scope", "codec": () => codec_CreatePlanInputBody_scope}, "services": {"ts": "services", "codec": () => codec_CreatePlanInputBody_services}}, "required": ["environment"]};
