import type { OperationMeta } from '../ops';
import { codec_RetentionPreview } from '../codecs/RetentionPreview';

export const op_nativeRecoverySources_previewRetention = {
  method: 'GET',
  path: '/api/v1/native-recovery-sources/{id}/retention-preview',
  provides: [],
  invalidates: [],
  responseCodec: codec_RetentionPreview,
} as const satisfies OperationMeta;
