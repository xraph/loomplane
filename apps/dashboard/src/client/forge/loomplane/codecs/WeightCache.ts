import type { Codec } from '../codec-runtime';

export const codec_WeightCache: Codec = {"kind": "object", "fields": {"kubernetes_size_mb": {"ts": "kubernetesSizeMb"}, "mount_path": {"ts": "mountPath"}}, "required": ["kubernetes_size_mb", "mount_path"]};
