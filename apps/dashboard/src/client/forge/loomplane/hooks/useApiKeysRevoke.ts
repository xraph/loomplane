import { mutation } from '@forge-go/client-core';
import { op_apiKeys_revoke } from '../ops/apiKeys.revoke';
import type { APIKey, CompromisedRevoke } from '../types';

export const useApiKeysRevoke = /*#__PURE__*/ mutation<CompromisedRevoke, APIKey>(op_apiKeys_revoke);
