import type { Codec } from '../codec-runtime';
import { codec_CLIOrganization } from './CLIOrganization';

export const codec_CLIOrganizationList_organizations: Codec = {"kind": "array", "items": () => codec_CLIOrganization};
