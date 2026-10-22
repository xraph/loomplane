import type { Codec } from '../codec-runtime';

export const codec_Volume: Codec = {"kind": "object", "fields": {"kubernetes_size_mb": {"ts": "kubernetesSizeMb"}, "mount_path": {"ts": "mountPath"}}, "required": ["mount_path"]};
