import type { OperationMeta } from '../ops';
import { codec_NativeRecoverySources } from '../codecs/NativeRecoverySources';

export const op_nativeRecoverySources_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/native-recovery-sources',
  entity: 'NativeSource',
  rootType: 'NativeRecoverySources',
  provides: ['NativeSource:{id}', 'NativeSource[]'],
  invalidates: [],
  responseCodec: codec_NativeRecoverySources,
} as const satisfies OperationMeta;
