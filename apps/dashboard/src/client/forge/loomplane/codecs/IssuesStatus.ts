import type { Codec } from '../codec-runtime';
import { codec_IssuesStatus_installations } from './IssuesStatus.installations';

export const codec_IssuesStatus: Codec = {"kind": "object", "fields": {"configured": {"ts": "configured"}, "installations": {"ts": "installations", "codec": () => codec_IssuesStatus_installations}}, "required": ["configured", "installations"]};
