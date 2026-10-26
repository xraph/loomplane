import type { Codec } from '../codec-runtime';
import { codec_InstanceRouteStats } from './InstanceRouteStats';

export const codec_InstanceStatus_routes: Codec = {"kind": "array", "items": () => codec_InstanceRouteStats};
