import type { OperationMeta } from '../ops';
import { codec_GroupDestinationMappingInputBody } from '../codecs/GroupDestinationMappingInputBody';
import { codec_GroupVerificationResult } from '../codecs/GroupVerificationResult';

export const op_placementGroups_destinationMapping = {
  method: 'POST',
  path: '/api/v1/projects/{id}/placement-groups/destination-mappings',
  entity: 'GroupVerificationResult',
  rootType: 'GroupVerificationResult',
  provides: ['GroupVerificationResult:{id}'],
  invalidates: ['GroupVerificationResult[]'],
  bodyCodec: codec_GroupDestinationMappingInputBody,
  responseCodec: codec_GroupVerificationResult,
} as const satisfies OperationMeta;
