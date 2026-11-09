import type { Codec } from '../codec-runtime';
import { codec_CLIOrganizationList_organizations } from './CLIOrganizationList.organizations';

export const codec_CLIOrganizationList: Codec = {"kind": "object", "fields": {"active": {"ts": "active"}, "organizations": {"ts": "organizations", "codec": () => codec_CLIOrganizationList_organizations}}, "required": ["organizations"]};
