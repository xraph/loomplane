import type { Codec } from '../codec-runtime';
import { codec_Action_changes } from './Action.changes';
import { codec_Action_depends_on } from './Action.depends_on';
import { codec_Action_routes } from './Action.routes';
import { codec_CloudAdoption } from './CloudAdoption';
import { codec_CloudCompensation } from './CloudCompensation';
import { codec_ResourceConfig } from './ResourceConfig';
import { codec_ServiceConfig } from './ServiceConfig';

export const codec_Action: Codec = {"kind": "object", "fields": {"adopt_cloud_placement": {"ts": "adoptCloudPlacement"}, "backup_every_hours": {"ts": "backupEveryHours"}, "changes": {"ts": "changes", "codec": () => codec_Action_changes}, "cloud_adoption": {"ts": "cloudAdoption", "codec": () => codec_CloudAdoption}, "compensation": {"ts": "compensation", "codec": () => codec_CloudCompensation}, "connection_id": {"ts": "connectionId"}, "creates": {"ts": "creates"}, "depends_on": {"ts": "dependsOn", "codec": () => codec_Action_depends_on}, "engine": {"ts": "engine"}, "id": {"ts": "id"}, "name": {"ts": "name"}, "op": {"ts": "op"}, "provider": {"ts": "provider"}, "reason": {"ts": "reason"}, "region": {"ts": "region"}, "resource": {"ts": "resource", "codec": () => codec_ResourceConfig}, "routes": {"ts": "routes", "codec": () => codec_Action_routes}, "service": {"ts": "service", "codec": () => codec_ServiceConfig}, "step": {"ts": "step"}, "subject": {"ts": "subject"}}, "required": ["creates", "engine", "id", "name", "op", "provider", "reason", "step", "subject"]};
