import { query } from '@forge-go/client-core';
import { op_connectionStorage_list } from '../ops/connectionStorage.list';
import type { StorageVolumeList } from '../types';

export const useConnectionStorageList = /*#__PURE__*/ query<StorageVolumeList>(op_connectionStorage_list);
