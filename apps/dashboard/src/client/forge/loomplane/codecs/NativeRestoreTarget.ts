import type { Codec } from '../codec-runtime';

export const codec_NativeRestoreTarget: Codec = {"kind": "object", "fields": {"account_identity": {"ts": "accountIdentity"}, "cloud_id": {"ts": "cloudId"}, "cloud_revision": {"ts": "cloudRevision"}, "connection_id": {"ts": "connectionId"}, "grant_revision": {"ts": "grantRevision"}, "native_project": {"ts": "nativeProject"}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "target_id": {"ts": "targetId"}, "target_revision": {"ts": "targetRevision"}}, "required": ["account_identity", "connection_id", "provider", "region"]};
