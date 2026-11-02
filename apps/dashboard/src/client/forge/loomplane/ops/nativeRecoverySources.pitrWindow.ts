import type { OperationMeta } from '../ops';
import { codec_NativePITRWindow } from '../codecs/NativePITRWindow';

export const op_nativeRecoverySources_pitrWindow = {
  method: 'GET',
  path: '/api/v1/native-recovery-sources/{id}/pitr-window',
  provides: [],
  invalidates: [],
  responseCodec: codec_NativePITRWindow,
} as const satisfies OperationMeta;
