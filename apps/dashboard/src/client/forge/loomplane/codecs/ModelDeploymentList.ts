import type { Codec } from '../codec-runtime';
import { codec_ModelDeploymentList_models } from './ModelDeploymentList.models';

export const codec_ModelDeploymentList: Codec = {"kind": "object", "fields": {"models": {"ts": "models", "codec": () => codec_ModelDeploymentList_models}}, "required": ["models"]};
