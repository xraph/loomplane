import type { Codec } from '../codec-runtime';

export const codec_PlanSummary: Codec = {"kind": "object", "fields": {"adopt": {"ts": "adopt"}, "create": {"ts": "create"}, "delete": {"ts": "delete"}, "keep": {"ts": "keep"}, "replace": {"ts": "replace"}, "update": {"ts": "update"}}, "required": ["adopt", "create", "delete", "keep", "replace", "update"]};
