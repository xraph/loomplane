import { query } from '@forge-go/client-core';
import { op_connectionClouds_list } from '../ops/connectionClouds.list';
import type { CloudList } from '../types';

export const useConnectionCloudsList = /*#__PURE__*/ query<CloudList>(op_connectionClouds_list);
