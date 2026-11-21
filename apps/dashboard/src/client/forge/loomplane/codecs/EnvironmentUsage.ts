import type { Codec } from '../codec-runtime';
import { codec_EnvironmentUsage_placements } from './EnvironmentUsage.placements';

export const codec_EnvironmentUsage: Codec = {"kind": "object", "fields": {"default": {"ts": "default"}, "environment_id": {"ts": "environmentId"}, "environment_name": {"ts": "environmentName"}, "placements": {"ts": "placements", "codec": () => codec_EnvironmentUsage_placements}, "project_id": {"ts": "projectId"}, "project_name": {"ts": "projectName"}}, "required": ["default", "environment_id", "environment_name", "placements", "project_id", "project_name"]};
