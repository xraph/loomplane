import type { Codec } from '../codec-runtime';

export const codec_PlanningSummary: Codec = {"kind": "object", "fields": {"create": {"ts": "create"}, "delete": {"ts": "delete"}, "keep": {"ts": "keep"}, "replace": {"ts": "replace"}, "update": {"ts": "update"}}, "required": ["create", "delete", "keep", "replace", "update"]};
