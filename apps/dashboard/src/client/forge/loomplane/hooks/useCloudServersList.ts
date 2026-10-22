import { query } from '@forge-go/client-core';
import { op_cloudServers_list } from '../ops/cloudServers.list';
import type { ServerList } from '../types';

export const useCloudServersList = /*#__PURE__*/ query<ServerList>(op_cloudServers_list);
