import { mutation } from '@forge-go/client-core';
import { op_bindings_setAlias } from '../ops/bindings.setAlias';
import type { Binding } from '../types';

export const useBindingsSetAlias = /*#__PURE__*/ mutation<Binding, Binding>(op_bindings_setAlias);
