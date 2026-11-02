import type { Codec } from '../codec-runtime';
import { codec_GPUDevice } from './GPUDevice';

export const codec_RuntimeHealth_gpus: Codec = {"kind": "array", "items": () => codec_GPUDevice};
