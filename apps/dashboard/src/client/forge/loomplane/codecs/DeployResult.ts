import type { Codec } from '../codec-runtime';
import { codec_ModelsDeployment } from './ModelsDeployment';

export const codec_DeployResult: Codec = {"kind": "object", "fields": {"deployment": {"ts": "deployment", "codec": () => codec_ModelsDeployment}, "message": {"ts": "message"}, "plan_id": {"ts": "planId"}}};
