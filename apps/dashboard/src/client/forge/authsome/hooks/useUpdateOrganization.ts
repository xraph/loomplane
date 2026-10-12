import { mutation } from '@forge-go/client-core';
import { op_updateOrganization } from '../ops/updateOrganization';
import type { Organization } from '../types';

export const useUpdateOrganization = /*#__PURE__*/ mutation<Organization, Organization>(op_updateOrganization);
