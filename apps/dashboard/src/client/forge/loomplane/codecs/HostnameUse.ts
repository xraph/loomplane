import type { Codec } from '../codec-runtime';
import { codec_CloudinfraRouteStatus } from './CloudinfraRouteStatus';

export const codec_HostnameUse: Codec = {"kind": "object", "fields": {"environment_id": {"ts": "environmentId"}, "hostname": {"ts": "hostname"}, "port": {"ts": "port"}, "project_id": {"ts": "projectId"}, "project_name": {"ts": "projectName"}, "route_id": {"ts": "routeId"}, "runs_on": {"ts": "runsOn"}, "service": {"ts": "service"}, "status": {"ts": "status", "codec": () => codec_CloudinfraRouteStatus}}, "required": ["environment_id", "hostname", "port", "project_id", "project_name", "route_id", "runs_on", "service", "status"]};
