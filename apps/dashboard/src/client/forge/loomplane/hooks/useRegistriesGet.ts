import { query } from '@forge-go/client-core';
import { op_registries_get } from '../ops/registries.get';
import type { ManagedRegistryView } from '../types';

export const useRegistriesGet = /*#__PURE__*/ query<ManagedRegistryView>(op_registries_get);
