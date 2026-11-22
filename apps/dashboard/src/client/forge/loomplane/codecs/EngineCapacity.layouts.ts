import type { Codec } from '../codec-runtime';
import { codec_DatabaseLayout } from './DatabaseLayout';

export const codec_EngineCapacity_layouts: Codec = {"kind": "array", "items": () => codec_DatabaseLayout};
