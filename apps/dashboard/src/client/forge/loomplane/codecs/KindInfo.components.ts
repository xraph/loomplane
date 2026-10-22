import type { Codec } from '../codec-runtime';
import { codec_ComponentInfo } from './ComponentInfo';

export const codec_KindInfo_components: Codec = {"kind": "array", "items": () => codec_ComponentInfo};
