import type { Codec } from '../codec-runtime';
import { codec_FrontendLimit } from './FrontendLimit';

export const codec_CapacityPolicyInputBody_policy_frontend_limits: Codec = {"kind": "array", "items": () => codec_FrontendLimit};
