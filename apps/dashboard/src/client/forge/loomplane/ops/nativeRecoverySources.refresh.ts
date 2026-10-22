import type { OperationMeta } from '../ops';
import { codec_NativeInventory } from '../codecs/NativeInventory';

export const op_nativeRecoverySources_refresh = {
  method: 'POST',
  path: '/api/v1/native-recovery-sources/{id}/refresh',
  rootType: 'NativeInventory',
  provides: [],
  invalidates: [],
  responseCodec: codec_NativeInventory,
} as const satisfies OperationMeta;
