import type { Codec } from '../codec-runtime';
import { codec_Resolution_variables } from './Resolution.variables';

export const codec_Resolution: Codec = {"kind": "object", "fields": {"environment": {"ts": "environment"}, "project_id": {"ts": "projectId"}, "service": {"ts": "service"}, "variables": {"ts": "variables", "codec": () => codec_Resolution_variables}}, "required": ["environment", "project_id", "variables"]};
