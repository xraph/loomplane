import type { Codec } from '../codec-runtime';
import { codec_CloudClusterOperation } from './CloudClusterOperation';

export const codec_CloudClusterOperationPage_items: Codec = {"kind": "array", "items": () => codec_CloudClusterOperation};
