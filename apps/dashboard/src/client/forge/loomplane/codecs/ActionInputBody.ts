import type { Codec } from '../codec-runtime';
import { codec_ActionInputBody_delete } from './ActionInputBody.delete';
import { codec_ActionInputBody_move } from './ActionInputBody.move';
import { codec_ActionInputBody_resize } from './ActionInputBody.resize';
import { codec_ActionInputBody_restore } from './ActionInputBody.restore';
import { codec_ActionInputBody_retire } from './ActionInputBody.retire';
import { codec_ActionInputBody_scale } from './ActionInputBody.scale';
import { codec_ActionInputBody_update } from './ActionInputBody.update';

export const codec_ActionInputBody: Codec = {"kind": "object", "fields": {"delete": {"ts": "delete", "codec": () => codec_ActionInputBody_delete}, "expected_generation": {"ts": "expectedGeneration"}, "expected_revision": {"ts": "expectedRevision"}, "idempotency_key": {"ts": "idempotencyKey"}, "kind": {"ts": "kind"}, "move": {"ts": "move", "codec": () => codec_ActionInputBody_move}, "resize": {"ts": "resize", "codec": () => codec_ActionInputBody_resize}, "restore": {"ts": "restore", "codec": () => codec_ActionInputBody_restore}, "resume": {"ts": "resume"}, "retire": {"ts": "retire", "codec": () => codec_ActionInputBody_retire}, "scale": {"ts": "scale", "codec": () => codec_ActionInputBody_scale}, "suspend": {"ts": "suspend"}, "update": {"ts": "update", "codec": () => codec_ActionInputBody_update}}, "required": ["expected_generation", "expected_revision", "idempotency_key", "kind"]};
