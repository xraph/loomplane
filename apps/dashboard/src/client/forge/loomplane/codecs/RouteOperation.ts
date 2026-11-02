import type { Codec } from '../codec-runtime';
import { codec_CloudinfraRoute } from './CloudinfraRoute';
import { codec_DNSRecord } from './DNSRecord';
import { codec_RouteCost } from './RouteCost';
import { codec_RouteOperation_desired_records } from './RouteOperation.desired_records';
import { codec_RouteOperation_notices } from './RouteOperation.notices';
import { codec_RouteOperation_receipts } from './RouteOperation.receipts';

export const codec_RouteOperation: Codec = {"kind": "object", "fields": {"attempt": {"ts": "attempt"}, "cost": {"ts": "cost", "codec": () => codec_RouteCost}, "created_at": {"ts": "createdAt"}, "desired_record": {"ts": "desiredRecord", "codec": () => codec_DNSRecord}, "desired_records": {"ts": "desiredRecords", "codec": () => codec_RouteOperation_desired_records}, "dns_attempted": {"ts": "dnsAttempted"}, "expires_at": {"ts": "expiresAt"}, "generation": {"ts": "generation"}, "handoff_id": {"ts": "handoffId"}, "id": {"ts": "id"}, "kind": {"ts": "kind"}, "message": {"ts": "message"}, "not_before": {"ts": "notBefore"}, "notices": {"ts": "notices", "codec": () => codec_RouteOperation_notices}, "previous_receipt": {"ts": "previousReceipt", "codec": () => codec_DNSRecord}, "receipt": {"ts": "receipt", "codec": () => codec_DNSRecord}, "receipts": {"ts": "receipts", "codec": () => codec_RouteOperation_receipts}, "review": {"ts": "review"}, "revision": {"ts": "revision"}, "route": {"ts": "route", "codec": () => codec_CloudinfraRoute}, "source": {"ts": "source", "codec": () => codec_CloudinfraRoute}, "status": {"ts": "status"}}, "required": ["attempt", "created_at", "desired_record", "dns_attempted", "expires_at", "generation", "id", "kind", "message", "not_before", "review", "revision", "route", "status"]};
