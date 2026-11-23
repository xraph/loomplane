import type { Codec } from '../codec-runtime';
import { codec_PoolSettings } from './PoolSettings';
import { codec_PoolView_runners } from './PoolView.runners';

export const codec_PoolView: Codec = {"kind": "object", "fields": {"architecture": {"ts": "architecture"}, "connection_id": {"ts": "connectionId"}, "created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "id": {"ts": "id"}, "mode": {"ts": "mode"}, "name": {"ts": "name"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "revision": {"ts": "revision"}, "runners": {"ts": "runners", "codec": () => codec_PoolView_runners}, "runtime_identity": {"ts": "runtimeIdentity"}, "scope": {"ts": "scope"}, "settings": {"ts": "settings", "codec": () => codec_PoolSettings}, "updated_at": {"ts": "updatedAt"}}, "required": ["created_at", "id", "mode", "name", "revision", "runners", "scope", "settings", "updated_at"]};
