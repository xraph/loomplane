import type { Codec } from '../codec-runtime';
import { codec_MoveInput } from './MoveInput';
import { codec_Placement } from './Placement';

export const codec_MoveReview: Codec = {"kind": "object", "fields": {"input": {"ts": "input", "codec": () => codec_MoveInput}, "placement": {"ts": "placement", "codec": () => codec_Placement}, "review": {"ts": "review"}, "source_server_id": {"ts": "sourceServerId"}}, "required": ["input", "placement", "review", "source_server_id"]};
