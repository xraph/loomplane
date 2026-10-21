import { query } from '@forge-go/client-core';
import { op_organizationClouds_listGrants } from '../ops/organizationClouds.listGrants';
import type { CloudProjectGrantList } from '../types';

export const useOrganizationCloudsListGrants = /*#__PURE__*/ query<CloudProjectGrantList>(op_organizationClouds_listGrants);
