import type { Codec } from '../codec-runtime';
import { codec_RetainedResource } from './RetainedResource';

export const codec_CloudCluster_retained_resources: Codec = {"kind": "array", "items": () => codec_RetainedResource};
