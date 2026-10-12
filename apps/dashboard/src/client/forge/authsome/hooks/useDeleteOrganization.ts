import { mutation } from '@forge-go/client-core';
import { op_deleteOrganization } from '../ops/deleteOrganization';

export const useDeleteOrganization = /*#__PURE__*/ mutation(op_deleteOrganization);
