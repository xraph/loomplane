import type { Codec } from '../codec-runtime';

export const codec_ConnectedRepository: Codec = {"kind": "object", "fields": {"default_branch": {"ts": "defaultBranch"}, "full_name": {"ts": "fullName"}, "installation": {"ts": "installation"}, "private": {"ts": "private"}, "url": {"ts": "url"}}, "required": ["default_branch", "full_name", "installation", "private", "url"]};
