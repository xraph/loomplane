import type { Codec } from '../codec-runtime';
import { codec_NativeInventory_points } from './NativeInventory.points';
import { codec_NativeSource } from './NativeSource';

export const codec_NativeInventory: Codec = {"kind": "object", "fields": {"next_before": {"ts": "nextBefore"}, "points": {"ts": "points", "codec": () => codec_NativeInventory_points}, "source": {"ts": "source", "codec": () => codec_NativeSource}}, "required": ["points", "source"]};
