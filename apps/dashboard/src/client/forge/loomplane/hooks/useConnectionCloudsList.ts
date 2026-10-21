import { query } from '@forge-go/client-core';
import { op_connectionClouds_list } from '../ops/connectionClouds.list';
import type { ConnectionsCloudList } from '../types';

export const useConnectionCloudsList = /*#__PURE__*/ query<ConnectionsCloudList>(op_connectionClouds_list);
