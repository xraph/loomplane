import type { Codec } from '../codec-runtime';
import { codec_Manifest } from './Manifest';

export const codec_Revision: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "digest": {"ts": "digest"}, "id": {"ts": "id"}, "manifest": {"ts": "manifest", "codec": () => codec_Manifest}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "revision": {"ts": "revision"}, "yaml": {"ts": "yaml"}}, "required": ["created_at", "created_by", "digest", "id", "org_id", "project_id", "revision", "yaml"]};
