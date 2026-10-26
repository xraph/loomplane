import type { Codec } from '../codec-runtime';
import { codec_CanaryService_refs } from './CanaryService.refs';

export const codec_CanaryService: Codec = {"kind": "object", "fields": {"action_id": {"ts": "actionId"}, "baseline": {"ts": "baseline"}, "binding_id": {"ts": "bindingId"}, "candidate": {"ts": "candidate"}, "image": {"ts": "image"}, "name": {"ts": "name"}, "provider": {"ts": "provider"}, "refs": {"ts": "refs", "codec": () => codec_CanaryService_refs}}, "required": ["action_id", "binding_id", "name", "provider"]};
