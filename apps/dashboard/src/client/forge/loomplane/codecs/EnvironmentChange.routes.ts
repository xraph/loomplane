import type { Codec } from '../codec-runtime';
import { codec_SyncRoute } from './SyncRoute';

export const codec_EnvironmentChange_routes: Codec = {"kind": "array", "items": () => codec_SyncRoute};
