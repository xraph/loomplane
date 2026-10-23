import type { Codec } from '../codec-runtime';
import { codec_DependencyInfo } from './DependencyInfo';

export const codec_KindInfo_dependencies: Codec = {"kind": "array", "items": () => codec_DependencyInfo};
