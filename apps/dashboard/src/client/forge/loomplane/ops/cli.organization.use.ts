import type { OperationMeta } from '../ops';
import { codec_CLIOrganizationList } from '../codecs/CLIOrganizationList';
import { codec_UseOrganizationInputBody } from '../codecs/UseOrganizationInputBody';

export const op_cli_organization_use = {
  method: 'PUT',
  path: '/api/v1/cli/organization',
  rootType: 'CLIOrganizationList',
  provides: [],
  invalidates: [],
  bodyCodec: codec_UseOrganizationInputBody,
  responseCodec: codec_CLIOrganizationList,
} as const satisfies OperationMeta;
