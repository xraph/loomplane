import type { Codec } from '../codec-runtime';
import { codec_Recipe_extensions } from './Recipe.extensions';

export const codec_Recipe: Codec = {"kind": "object", "fields": {"architecture": {"ts": "architecture"}, "backup_format": {"ts": "backupFormat"}, "extensions": {"ts": "extensions", "codec": () => codec_Recipe_extensions}, "family": {"ts": "family"}, "fs_group": {"ts": "fsGroup"}, "id": {"ts": "id"}, "image": {"ts": "image"}, "mount_path": {"ts": "mountPath"}, "product_version": {"ts": "productVersion"}, "revision": {"ts": "revision"}, "volume_size_mb": {"ts": "volumeSizeMb"}}, "required": ["architecture", "backup_format", "id", "image", "mount_path", "product_version", "revision"]};
