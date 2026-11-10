import type { Codec } from '../codec-runtime';
import { codec_CloudinfraRoute } from './CloudinfraRoute';

export const codec_Action_removes_routes: Codec = {"kind": "array", "items": () => codec_CloudinfraRoute};
