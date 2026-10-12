import { mutation } from '@forge-go/client-core';
import { op_createInvitation } from '../ops/createInvitation';
import type { Invitation } from '../types';

export const useCreateInvitation = /*#__PURE__*/ mutation<Invitation, Invitation>(op_createInvitation);
