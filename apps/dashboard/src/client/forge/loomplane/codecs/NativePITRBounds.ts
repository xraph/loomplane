import type { Codec } from '../codec-runtime';

export const codec_NativePITRBounds: Codec = {"kind": "object", "fields": {"earliest": {"ts": "earliest"}, "latest": {"ts": "latest"}, "protocol": {"ts": "protocol"}, "settings_digest": {"ts": "settingsDigest"}, "source_backup": {"ts": "sourceBackup"}, "source_deleted_at": {"ts": "sourceDeletedAt"}}, "required": ["earliest", "latest", "protocol"]};
