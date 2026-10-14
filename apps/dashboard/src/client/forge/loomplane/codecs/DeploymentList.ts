import type { Codec } from '../codec-runtime';
import { codec_DeploymentList_deployments } from './DeploymentList.deployments';

export const codec_DeploymentList: Codec = {"kind": "object", "fields": {"deployments": {"ts": "deployments", "codec": () => codec_DeploymentList_deployments}, "next": {"ts": "next"}}, "required": ["deployments"]};
