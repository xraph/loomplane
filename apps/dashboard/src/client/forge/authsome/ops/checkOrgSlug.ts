import type { OperationMeta } from '../ops';
import { codec_SlugAvailableResponse } from '../codecs/SlugAvailableResponse';

export const op_checkOrgSlug = {
  method: 'GET',
  path: '/authsome/v1/orgs/check-slug',
  provides: [],
  invalidates: [],
  security: ['bearerAuth'],
  responseCodec: codec_SlugAvailableResponse,
} as const satisfies OperationMeta;
