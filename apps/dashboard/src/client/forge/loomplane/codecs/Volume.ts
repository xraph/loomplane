import type { Codec } from '../codec-runtime';

export const codec_Volume: Codec = {"kind": "object", "fields": {"fs_group": {"ts": "fsGroup"}, "kubernetes_size_mb": {"ts": "kubernetesSizeMb"}, "mount_path": {"ts": "mountPath"}}, "required": ["mount_path"]};
