import type { Codec } from '../codec-runtime';

export const codec_MigrationFile: Codec = {"kind": "object", "fields": {"additions": {"ts": "additions"}, "deletions": {"ts": "deletions"}, "patch": {"ts": "patch"}, "path": {"ts": "path"}, "status": {"ts": "status"}, "truncated": {"ts": "truncated"}}, "required": ["additions", "deletions", "patch", "path", "status", "truncated"]};
