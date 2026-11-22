import type { Codec } from '../codec-runtime';
import { codec_EngineCapacity_layouts } from './EngineCapacity.layouts';
import { codec_EngineCapacity_regions } from './EngineCapacity.regions';

export const codec_EngineCapacity: Codec = {"kind": "object", "fields": {"layouts": {"ts": "layouts", "codec": () => codec_EngineCapacity_layouts}, "regions": {"ts": "regions", "codec": () => codec_EngineCapacity_regions}}, "required": ["layouts", "regions"]};
