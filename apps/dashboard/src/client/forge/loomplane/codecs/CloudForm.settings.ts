import type { Codec } from '../codec-runtime';
import { codec_Setting } from './Setting';

export const codec_CloudForm_settings: Codec = {"kind": "array", "items": () => codec_Setting};
