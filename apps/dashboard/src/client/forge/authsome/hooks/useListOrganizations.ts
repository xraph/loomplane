import { query } from '@forge-go/client-core';
import { op_listOrganizations } from '../ops/listOrganizations';
import type { OrgListResponse } from '../types';

export const useListOrganizations = /*#__PURE__*/ query<OrgListResponse>(op_listOrganizations);
