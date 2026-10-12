import { mutation } from '@forge-go/client-core';
import { op_acceptInvitation } from '../ops/acceptInvitation';
import type { Member } from '../types';

export const useAcceptInvitation = /*#__PURE__*/ mutation<Member, Member>(op_acceptInvitation);
