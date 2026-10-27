import type { Codec } from '../codec-runtime';
import { codec_Setting } from './Setting';

export const codec_CloudForm_in_cluster_settings: Codec = {"kind": "array", "items": () => codec_Setting};
