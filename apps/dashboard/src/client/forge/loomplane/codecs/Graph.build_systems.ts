import type { Codec } from '../codec-runtime';
import { codec_BuildSystem } from './BuildSystem';

export const codec_Graph_build_systems: Codec = {"kind": "array", "items": () => codec_BuildSystem};
