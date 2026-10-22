import { query } from '@forge-go/client-core';
import { op_nativeRecoverySources_inventory } from '../ops/nativeRecoverySources.inventory';
import type { NativeInventory } from '../types';

export const useNativeRecoverySourcesInventory = /*#__PURE__*/ query<NativeInventory>(op_nativeRecoverySources_inventory);
