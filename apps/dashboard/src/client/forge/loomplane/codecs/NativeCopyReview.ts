import type { Codec } from '../codec-runtime';
import { codec_NativeCopyCapacity } from './NativeCopyCapacity';
import { codec_NativeRestoreTarget } from './NativeRestoreTarget';

export const codec_NativeCopyReview: Codec = {"kind": "object", "fields": {"capacity": {"ts": "capacity", "codec": () => codec_NativeCopyCapacity}, "notice": {"ts": "notice"}, "point_id": {"ts": "pointId"}, "request_id": {"ts": "requestId"}, "review_token": {"ts": "reviewToken"}, "source_id": {"ts": "sourceId"}, "target": {"ts": "target", "codec": () => codec_NativeRestoreTarget}, "ttl_hours": {"ts": "ttlHours"}, "valid_until": {"ts": "validUntil"}}, "required": ["capacity", "notice", "point_id", "request_id", "review_token", "source_id", "target", "ttl_hours", "valid_until"]};
