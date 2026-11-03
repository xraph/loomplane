import type { Codec } from '../codec-runtime';

export const codec_GitLabRepository: Codec = {"kind": "object", "fields": {"connection": {"ts": "connection"}, "default_branch": {"ts": "defaultBranch"}, "full_name": {"ts": "fullName"}, "host": {"ts": "host"}, "key": {"ts": "key"}, "name": {"ts": "name"}, "namespace": {"ts": "namespace"}, "private": {"ts": "private"}}, "required": ["connection", "default_branch", "full_name", "host", "key", "name", "namespace", "private"]};
