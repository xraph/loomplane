import type { Codec } from '../codec-runtime';
import { codec_CloudCluster } from './CloudCluster';

export const codec_CloudClusterPage_items: Codec = {"kind": "array", "items": () => codec_CloudCluster};
