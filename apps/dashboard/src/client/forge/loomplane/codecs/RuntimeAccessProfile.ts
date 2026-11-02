import type { Codec } from '../codec-runtime';

export const codec_RuntimeAccessProfile: Codec = {"kind": "object", "fields": {"cleanup_dependents": {"ts": "cleanupDependents"}, "hpa": {"ts": "hpa"}, "schema_version": {"ts": "schemaVersion"}}, "required": ["cleanup_dependents", "hpa", "schema_version"]};
