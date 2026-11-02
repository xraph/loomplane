import type { OperationMeta } from '../ops';
import { codec_NativeCopyCredentials } from '../codecs/NativeCopyCredentials';

export const op_nativeRecoveryCopies_credentials = {
  method: 'POST',
  path: '/api/v1/native-recovery-copies/{id}/credentials',
  provides: [],
  invalidates: [],
  responseCodec: codec_NativeCopyCredentials,
} as const satisfies OperationMeta;
