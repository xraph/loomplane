import type { Codec } from '../codec-runtime';
import { codec_Step } from './Step';

export const codec_Deployment_steps: Codec = {"kind": "array", "items": () => codec_Step};
