import type { OperationMeta } from '../ops';
import { codec_MemberPrerequisite } from '../codecs/MemberPrerequisite';
import { codec_MemberPrerequisiteInputBody } from '../codecs/MemberPrerequisiteInputBody';

export const op_placementGroups_prerequisite = {
  method: 'POST',
  path: '/api/v1/projects/{id}/placement-groups/prerequisites',
  entity: 'MemberPrerequisite',
  rootType: 'MemberPrerequisite',
  provides: ['MemberPrerequisite:{id}'],
  invalidates: ['MemberPrerequisite[]'],
  bodyCodec: codec_MemberPrerequisiteInputBody,
  responseCodec: codec_MemberPrerequisite,
} as const satisfies OperationMeta;
