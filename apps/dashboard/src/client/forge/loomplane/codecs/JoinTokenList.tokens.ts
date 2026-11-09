import type { Codec } from '../codec-runtime';
import { codec_PendingNodeJoinToken } from './PendingNodeJoinToken';

export const codec_JoinTokenList_tokens: Codec = {"kind": "array", "items": () => codec_PendingNodeJoinToken};
