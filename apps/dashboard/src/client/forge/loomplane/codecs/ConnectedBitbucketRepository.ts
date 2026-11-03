import type { Codec } from '../codec-runtime';

export const codec_ConnectedBitbucketRepository: Codec = {"kind": "object", "fields": {"default_branch": {"ts": "defaultBranch"}, "full_name": {"ts": "fullName"}, "private": {"ts": "private"}, "workspace": {"ts": "workspace"}}, "required": ["default_branch", "full_name", "private", "workspace"]};
