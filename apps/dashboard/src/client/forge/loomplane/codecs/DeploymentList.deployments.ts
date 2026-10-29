import type { Codec } from '../codec-runtime';
import { codec_DeploymentsDeployment } from './DeploymentsDeployment';

export const codec_DeploymentList_deployments: Codec = {"kind": "array", "items": () => codec_DeploymentsDeployment};
