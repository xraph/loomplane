import type { Codec } from '../codec-runtime';

export const codec_PolicyInputBody: Codec = {"kind": "object", "fields": {"enabled": {"ts": "enabled"}, "environment": {"ts": "environment"}, "every_hours": {"ts": "everyHours"}, "resource": {"ts": "resource"}}, "required": ["enabled", "environment", "every_hours", "resource"]};
