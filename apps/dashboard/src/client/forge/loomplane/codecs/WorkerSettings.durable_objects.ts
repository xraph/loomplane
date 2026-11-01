import type { Codec } from '../codec-runtime';
import { codec_DurableObjectClass } from './DurableObjectClass';

export const codec_WorkerSettings_durable_objects: Codec = {"kind": "array", "items": () => codec_DurableObjectClass};
