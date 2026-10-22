import type { Codec } from '../codec-runtime';
import { codec_NativePoint } from './NativePoint';

export const codec_NativeInventory_points: Codec = {"kind": "array", "items": () => codec_NativePoint};
