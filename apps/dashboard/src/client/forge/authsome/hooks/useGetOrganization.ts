import { query } from '@forge-go/client-core';
import { op_getOrganization } from '../ops/getOrganization';
import type { Organization } from '../types';

export const useGetOrganization = /*#__PURE__*/ query<Organization>(op_getOrganization);
