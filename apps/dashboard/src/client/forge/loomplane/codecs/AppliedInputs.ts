import type { Codec } from '../codec-runtime';
import { codec_PlanSource } from './PlanSource';

export const codec_AppliedInputs: Codec = {"kind": "object", "fields": {"artifact_key": {"ts": "artifactKey"}, "source": {"ts": "source", "codec": () => codec_PlanSource}, "variables": {"ts": "variables"}}};
