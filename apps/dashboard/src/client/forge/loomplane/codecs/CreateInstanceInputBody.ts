import type { Codec } from '../codec-runtime';
import { codec_CreateInstanceInputBody_parameters } from './CreateInstanceInputBody.parameters';
import { codec_CreateInstanceInputBody_secret_refs } from './CreateInstanceInputBody.secret_refs';

export const codec_CreateInstanceInputBody: Codec = {"kind": "object", "fields": {"external_ref": {"ts": "externalRef"}, "idempotency_key": {"ts": "idempotencyKey"}, "parameters": {"ts": "parameters", "codec": () => codec_CreateInstanceInputBody_parameters}, "profile_id": {"ts": "profileId"}, "release_id": {"ts": "releaseId"}, "secret_refs": {"ts": "secretRefs", "codec": () => codec_CreateInstanceInputBody_secret_refs}, "stage_id": {"ts": "stageId"}}, "required": ["external_ref", "idempotency_key", "profile_id", "release_id", "stage_id"]};
