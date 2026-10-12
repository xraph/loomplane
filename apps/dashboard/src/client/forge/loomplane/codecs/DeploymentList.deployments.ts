import type { Codec } from '../codec-runtime';
import { codec_Deployment } from './Deployment';

export const codec_DeploymentList_deployments: Codec = {"kind": "array", "items": () => codec_Deployment};
