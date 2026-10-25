import { mutation } from '@forge-go/client-core';
import { op_fabriqRebuilds_create } from '../ops/fabriqRebuilds.create';
import type { Rebuild } from '../types';

export const useFabriqRebuildsCreate = /*#__PURE__*/ mutation<Rebuild, Rebuild>(op_fabriqRebuilds_create);
