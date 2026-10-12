import { query } from '@forge-go/client-core';
import { op_apiKeys_list } from '../ops/apiKeys.list';
import type { APIKeyList } from '../types';

export const useApiKeysList = /*#__PURE__*/ query<APIKeyList>(op_apiKeys_list);
