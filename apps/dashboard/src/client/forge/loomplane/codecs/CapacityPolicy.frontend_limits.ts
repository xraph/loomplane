import type { Codec } from '../codec-runtime';
import { codec_FrontendLimit } from './FrontendLimit';

export const codec_CapacityPolicy_frontend_limits: Codec = {"kind": "array", "items": () => codec_FrontendLimit};
