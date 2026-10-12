import type { Codec } from '../codec-runtime';
import { codec_Organization } from './Organization';

export const codec_OrgListResponse_organizations: Codec = {"kind": "array", "items": () => codec_Organization};
