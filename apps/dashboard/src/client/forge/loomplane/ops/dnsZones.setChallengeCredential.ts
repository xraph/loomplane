import type { OperationMeta } from '../ops';
import { codec_ChallengeCredentialInputBody } from '../codecs/ChallengeCredentialInputBody';
import { codec_Zone } from '../codecs/Zone';

export const op_dnsZones_setChallengeCredential = {
  method: 'POST',
  path: '/api/v1/dns-zones/{zoneId}/challenge-credential',
  entity: 'Zone',
  rootType: 'Zone',
  provides: ['Zone:{id}'],
  invalidates: ['Zone[]'],
  bodyCodec: codec_ChallengeCredentialInputBody,
  responseCodec: codec_Zone,
} as const satisfies OperationMeta;
