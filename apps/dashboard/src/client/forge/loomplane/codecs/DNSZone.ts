import type { Codec } from '../codec-runtime';
import { codec_DNSZone_nameservers } from './DNSZone.nameservers';

export const codec_DNSZone: Codec = {"kind": "object", "fields": {"account_id": {"ts": "accountId"}, "id": {"ts": "id"}, "name": {"ts": "name"}, "nameservers": {"ts": "nameservers", "codec": () => codec_DNSZone_nameservers}, "verified": {"ts": "verified"}}, "required": ["account_id", "id", "name", "verified"]};
