import type { Codec } from '../codec-runtime';
import { codec_Host } from './Host';
import { codec_MoveAll_failed } from './MoveAll.failed';
import { codec_MoveAll_skipped } from './MoveAll.skipped';

export const codec_MoveAll: Codec = {"kind": "object", "fields": {"failed": {"ts": "failed", "codec": () => codec_MoveAll_failed}, "host": {"ts": "host", "codec": () => codec_Host}, "moved": {"ts": "moved"}, "skipped": {"ts": "skipped", "codec": () => codec_MoveAll_skipped}}, "required": ["failed", "host", "moved", "skipped"]};
