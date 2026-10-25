import type { Codec } from '../codec-runtime';

export const codec_Status: Codec = {"kind": "object", "fields": {"entity_types": {"ts": "entityTypes"}, "keys_report_use": {"ts": "keysReportUse"}, "kind": {"ts": "kind"}, "migrations": {"ts": "migrations"}, "migrations_known": {"ts": "migrationsKnown"}, "reachable": {"ts": "reachable"}, "reason": {"ts": "reason"}, "version": {"ts": "version"}}, "required": ["entity_types", "keys_report_use", "kind", "migrations", "migrations_known", "reachable", "version"]};
