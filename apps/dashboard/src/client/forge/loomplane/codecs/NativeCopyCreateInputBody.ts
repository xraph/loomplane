import type { Codec } from '../codec-runtime';

export const codec_NativeCopyCreateInputBody: Codec = {"kind": "object", "fields": {"acknowledge_charges": {"ts": "acknowledgeCharges"}, "request_id": {"ts": "requestId"}, "review_token": {"ts": "reviewToken"}}, "required": ["acknowledge_charges", "request_id", "review_token"]};
