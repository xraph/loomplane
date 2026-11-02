import { query } from '@forge-go/client-core';
import { op_organizationClouds_usage } from '../ops/organizationClouds.usage';
import type { Usage } from '../types';

export const useOrganizationCloudsUsage = /*#__PURE__*/ query<Usage>(op_organizationClouds_usage);
