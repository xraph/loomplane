import type { OperationMeta } from '../ops';
import { codec_CLIOrganizationList } from '../codecs/CLIOrganizationList';

export const op_cli_organizations_list = {
  method: 'GET',
  path: '/api/v1/cli/organizations',
  entity: 'CLIOrganization',
  rootType: 'CLIOrganizationList',
  provides: ['CLIOrganization:{id}', 'CLIOrganization[]'],
  invalidates: [],
  responseCodec: codec_CLIOrganizationList,
} as const satisfies OperationMeta;
