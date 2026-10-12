import { mutation } from '@forge-go/client-core';
import { op_apiKeys_create } from '../ops/apiKeys.create';

export const useApiKeysCreate = /*#__PURE__*/ mutation(op_apiKeys_create);
