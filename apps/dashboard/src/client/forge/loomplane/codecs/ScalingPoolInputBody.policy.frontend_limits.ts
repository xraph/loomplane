import type { Codec } from '../codec-runtime';
import { codec_FrontendLimit } from './FrontendLimit';

export const codec_ScalingPoolInputBody_policy_frontend_limits: Codec = {"kind": "array", "items": () => codec_FrontendLimit};
