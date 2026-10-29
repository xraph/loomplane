import type { Codec } from '../codec-runtime';
import { codec_RollbackCommit } from './RollbackCommit';

export const codec_RollbackTarget_commits: Codec = {"kind": "array", "items": () => codec_RollbackCommit};
