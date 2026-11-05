import { query } from '@forge-go/client-core';
import { op_cli_organizations_list } from '../ops/cli.organizations.list';
import type { CLIOrganizationList } from '../types';

export const useCliOrganizationsList = /*#__PURE__*/ query<CLIOrganizationList>(op_cli_organizations_list);
