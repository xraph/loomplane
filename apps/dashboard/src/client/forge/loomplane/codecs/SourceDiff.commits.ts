import type { Codec } from '../codec-runtime';
import { codec_DiffCommit } from './DiffCommit';

export const codec_SourceDiff_commits: Codec = {"kind": "array", "items": () => codec_DiffCommit};
