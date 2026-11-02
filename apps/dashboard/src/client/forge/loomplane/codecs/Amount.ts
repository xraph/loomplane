import type { Codec } from '../codec-runtime';
import { codec_Amount_days } from './Amount.days';

export const codec_Amount: Codec = {"kind": "object", "fields": {"currency": {"ts": "currency"}, "days": {"ts": "days", "codec": () => codec_Amount_days}, "total": {"ts": "total"}, "undated": {"ts": "undated"}}, "required": ["currency", "days", "total", "undated"]};
