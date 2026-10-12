import { mutation } from '@forge-go/client-core';
import { op_apiKeys_revoke } from '../ops/apiKeys.revoke';

export const useApiKeysRevoke = /*#__PURE__*/ mutation(op_apiKeys_revoke);
