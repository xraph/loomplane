import type { Codec } from '../codec-runtime';

export const codec_NativeCopyCapacity: Codec = {"kind": "object", "fields": {"availability": {"ts": "availability"}, "compute_class": {"ts": "computeClass"}, "profile_id": {"ts": "profileId"}, "profile_revision": {"ts": "profileRevision"}, "storage_gb": {"ts": "storageGb"}, "storage_type": {"ts": "storageType"}}, "required": ["availability", "compute_class", "profile_id", "profile_revision", "storage_gb", "storage_type"]};
