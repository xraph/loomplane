import { mutation } from '@forge-go/client-core';
import { op_addMember } from '../ops/addMember';
import type { Member } from '../types';

export const useAddMember = /*#__PURE__*/ mutation<Member, Member>(op_addMember);
