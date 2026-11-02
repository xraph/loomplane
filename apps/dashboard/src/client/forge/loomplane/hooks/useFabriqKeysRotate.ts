import { mutation } from '@forge-go/client-core';
import { op_fabriqKeys_rotate } from '../ops/fabriqKeys.rotate';
import type { ConsumerKey } from '../types';

export const useFabriqKeysRotate = /*#__PURE__*/ mutation<ConsumerKey, ConsumerKey>(op_fabriqKeys_rotate);
