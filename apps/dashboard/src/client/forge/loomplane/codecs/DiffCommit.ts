import type { Codec } from '../codec-runtime';

export const codec_DiffCommit: Codec = {"kind": "object", "fields": {"at": {"ts": "at"}, "author": {"ts": "author"}, "sha": {"ts": "sha"}, "title": {"ts": "title"}}, "required": ["at", "author", "sha", "title"]};
