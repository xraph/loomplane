import { mutation } from '@forge-go/client-core';
import { op_updateMember } from '../ops/updateMember';
import type { Member } from '../types';

export const useUpdateMember = /*#__PURE__*/ mutation<Member, Member>(op_updateMember);
