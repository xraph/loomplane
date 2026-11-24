import { query } from '@forge-go/client-core';
import { op_catalogEndpoints_users } from '../ops/catalogEndpoints.users';
import type { OwnUserList } from '../types';

export const useCatalogEndpointsUsers = /*#__PURE__*/ query<OwnUserList>(op_catalogEndpoints_users);
