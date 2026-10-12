import type { Codec } from '../codec-runtime';
import { codec_Summary } from './Summary';

export const codec_Analysis: Codec = {"kind": "object", "fields": {"commit_sha": {"ts": "commitSha"}, "created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "error": {"ts": "error"}, "finished_at": {"ts": "finishedAt"}, "id": {"ts": "id"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "ref": {"ts": "ref"}, "repository": {"ts": "repository"}, "started_at": {"ts": "startedAt"}, "status": {"ts": "status"}, "summary": {"ts": "summary", "codec": () => codec_Summary}}, "required": ["commit_sha", "created_at", "created_by", "error", "id", "org_id", "project_id", "ref", "repository", "status", "summary"]};
