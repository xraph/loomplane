import type { OperationMeta } from '../ops';
import { codec_NativeInventory } from '../codecs/NativeInventory';

export const op_nativeRecoverySources_inventory = {
  method: 'GET',
  path: '/api/v1/native-recovery-sources/{id}',
  entity: 'NativePoint',
  rootType: 'NativeInventory',
  provides: ['NativePoint:{id}', 'NativePoint[]'],
  invalidates: [],
  responseCodec: codec_NativeInventory,
} as const satisfies OperationMeta;
