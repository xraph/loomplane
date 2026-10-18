import type { Codec } from '../codec-runtime';

export const codec_Recipe: Codec = {"kind": "object", "fields": {"architecture": {"ts": "architecture"}, "backup_format": {"ts": "backupFormat"}, "id": {"ts": "id"}, "image": {"ts": "image"}, "mount_path": {"ts": "mountPath"}, "product_version": {"ts": "productVersion"}, "revision": {"ts": "revision"}}, "required": ["architecture", "backup_format", "id", "image", "mount_path", "product_version", "revision"]};
