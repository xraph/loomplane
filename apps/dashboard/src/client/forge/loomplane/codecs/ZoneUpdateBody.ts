import type { Codec } from '../codec-runtime';
import { codec_ZoneUpdateBody_project_ids } from './ZoneUpdateBody.project_ids';

export const codec_ZoneUpdateBody: Codec = {"kind": "object", "fields": {"project_ids": {"ts": "projectIds", "codec": () => codec_ZoneUpdateBody_project_ids}, "renew_proof": {"ts": "renewProof"}, "revision": {"ts": "revision"}}, "required": ["project_ids", "renew_proof", "revision"]};
