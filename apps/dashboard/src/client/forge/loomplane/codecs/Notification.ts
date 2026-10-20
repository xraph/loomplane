import type { Codec } from '../codec-runtime';

export const codec_Notification: Codec = {"kind": "object", "fields": {"action_url": {"ts": "actionUrl"}, "body": {"ts": "body"}, "created_at": {"ts": "createdAt"}, "email_status": {"ts": "emailStatus"}, "id": {"ts": "id"}, "project_id": {"ts": "projectId"}, "read": {"ts": "read"}, "status": {"ts": "status"}, "title": {"ts": "title"}}, "required": ["action_url", "body", "created_at", "email_status", "id", "project_id", "read", "status", "title"]};
