import type { Codec } from '../codec-runtime';
import { codec_ModelsStatus } from './ModelsStatus';

export const codec_ModelDeploymentList_models: Codec = {"kind": "array", "items": () => codec_ModelsStatus};
