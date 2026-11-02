import { mutation } from '@forge-go/client-core';
import { op_fabriqKeys_revoke } from '../ops/fabriqKeys.revoke';
import type { ConsumerKey } from '../types';

export const useFabriqKeysRevoke = /*#__PURE__*/ mutation<ConsumerKey, ConsumerKey>(op_fabriqKeys_revoke);
