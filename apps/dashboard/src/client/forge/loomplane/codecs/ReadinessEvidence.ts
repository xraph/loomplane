import type { Codec } from '../codec-runtime';
import { codec_BudgetState } from './BudgetState';
import { codec_Coverage } from './Coverage';
import { codec_ReadinessEvidence_artifacts } from './ReadinessEvidence.artifacts';
import { codec_ReadinessEvidence_canaries } from './ReadinessEvidence.canaries';
import { codec_ReadinessEvidence_changed_services } from './ReadinessEvidence.changed_services';
import { codec_ReadinessEvidence_data_updates } from './ReadinessEvidence.data_updates';
import { codec_ReadinessEvidence_destroy } from './ReadinessEvidence.destroy';
import { codec_ReadinessEvidence_open_reports } from './ReadinessEvidence.open_reports';
import { codec_ReadinessEvidence_pin_differences } from './ReadinessEvidence.pin_differences';
import { codec_ReadinessEvidence_problems } from './ReadinessEvidence.problems';
import { codec_ReadinessEvidence_sources } from './ReadinessEvidence.sources';
import { codec_ReadinessEvidence_unavailable } from './ReadinessEvidence.unavailable';
import { codec_ReadinessEvidence_warnings } from './ReadinessEvidence.warnings';
import { codec_ReleaseEvidence } from './ReleaseEvidence';
import { codec_Soak } from './Soak';

export const codec_ReadinessEvidence: Codec = {"kind": "object", "fields": {"artifacts": {"ts": "artifacts", "codec": () => codec_ReadinessEvidence_artifacts}, "backup_schedule": {"ts": "backupSchedule"}, "budget": {"ts": "budget", "codec": () => codec_BudgetState}, "canaries": {"ts": "canaries", "codec": () => codec_ReadinessEvidence_canaries}, "changed_services": {"ts": "changedServices", "codec": () => codec_ReadinessEvidence_changed_services}, "collected_at": {"ts": "collectedAt"}, "coverage": {"ts": "coverage", "codec": () => codec_Coverage}, "data_updates": {"ts": "dataUpdates", "codec": () => codec_ReadinessEvidence_data_updates}, "destroy": {"ts": "destroy", "codec": () => codec_ReadinessEvidence_destroy}, "open_reports": {"ts": "openReports", "codec": () => codec_ReadinessEvidence_open_reports}, "pin_differences": {"ts": "pinDifferences", "codec": () => codec_ReadinessEvidence_pin_differences}, "pins_match": {"ts": "pinsMatch"}, "plan_status": {"ts": "planStatus"}, "problems": {"ts": "problems", "codec": () => codec_ReadinessEvidence_problems}, "production_release": {"ts": "productionRelease", "codec": () => codec_ReleaseEvidence}, "soak": {"ts": "soak", "codec": () => codec_Soak}, "sources": {"ts": "sources", "codec": () => codec_ReadinessEvidence_sources}, "staging_release": {"ts": "stagingRelease", "codec": () => codec_ReleaseEvidence}, "unavailable": {"ts": "unavailable", "codec": () => codec_ReadinessEvidence_unavailable}, "warnings": {"ts": "warnings", "codec": () => codec_ReadinessEvidence_warnings}}, "required": ["artifacts", "backup_schedule", "canaries", "changed_services", "collected_at", "data_updates", "destroy", "open_reports", "pin_differences", "pins_match", "plan_status", "problems", "sources", "unavailable", "warnings"]};
