import type { Codec } from '../codec-runtime';
import { codec_ReleaseRef } from './ReleaseRef';
import { codec_RollbackTarget_commits } from './RollbackTarget.commits';

export const codec_RollbackTarget: Codec = {"kind": "object", "fields": {"commits": {"ts": "commits", "codec": () => codec_RollbackTarget_commits}, "environment": {"ts": "environment"}, "restore": {"ts": "restore", "codec": () => codec_ReleaseRef}, "target": {"ts": "target", "codec": () => codec_ReleaseRef}}, "required": ["commits", "environment", "restore", "target"]};
