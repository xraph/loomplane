import type { Codec } from '../codec-runtime';
import { codec_AccountSpend_days } from './AccountSpend.days';

export const codec_AccountSpend: Codec = {"kind": "object", "fields": {"cloud": {"ts": "cloud"}, "connection_id": {"ts": "connectionId"}, "currency": {"ts": "currency"}, "days": {"ts": "days", "codec": () => codec_AccountSpend_days}, "name": {"ts": "name"}, "total": {"ts": "total"}}, "required": ["cloud", "connection_id", "currency", "days", "name", "total"]};
