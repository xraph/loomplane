import type { Codec } from '../codec-runtime';
import { codec_ActionInputBody_update_parameters } from './ActionInputBody.update.parameters';
import { codec_ActionInputBody_update_secret_refs } from './ActionInputBody.update.secret_refs';
import { codec_RolloutMemberPin } from './RolloutMemberPin';

export const codec_ActionInputBody_update: Codec = {"kind": "object", "fields": {"parameters": {"ts": "parameters", "codec": () => codec_ActionInputBody_update_parameters}, "release_id": {"ts": "releaseId"}, "rollout": {"ts": "rollout", "codec": () => codec_RolloutMemberPin}, "secret_refs": {"ts": "secretRefs", "codec": () => codec_ActionInputBody_update_secret_refs}}, "required": ["parameters", "release_id", "secret_refs"]};
