import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type {
  AssetLocation,
  Building,
  Page,
  Placement,
  Rack,
  RackDetail,
  Room,
  Tree,
  VM,
  VMState,
} from './types';
const e = encodeURIComponent;
const idPath = (base: string, id: string) => `${base}/${e(id)}`;
registerErrorMessages({
  'infrastructure.not_found': 'error.notFound',
  'infrastructure.conflict': 'infra.error.conflict',
  'infrastructure.version_conflict': 'error.versionConflict',
  'infrastructure.archived': 'infra.error.archived',
  'infrastructure.not_empty': 'infra.error.notEmpty',
  'infrastructure.occupied': 'infra.error.occupied',
  'infrastructure.asset_placed': 'infra.error.assetPlaced',
  'infrastructure.placement_closed': 'infra.error.closed',
  'infrastructure.decommissioned': 'infra.error.decommissioned',
});
registerErrorResolver((error) =>
  error.code.startsWith('infrastructure.invalid_') || error.code === 'infrastructure.asset_invalid'
    ? 'error.invalidRequest'
    : undefined,
);
export const infrastructureApi = {
  tree: (includeArchived = false, signal?: AbortSignal) =>
    api.get<Tree>('/infrastructure/tree', { query: { includeArchived }, signal }),
  buildings: (siteLocationId: string, cursor?: string, signal?: AbortSignal) =>
    api.get<Page<Building>>('/buildings', {
      query: { siteLocationId, cursor, limit: 200, includeArchived: true },
      signal,
    }),
  building: (id: string, signal?: AbortSignal) =>
    api.get<Building>(idPath('/buildings', id), { signal }),
  createBuilding: (body: { siteLocationId: string; name: string; addressNote: string }) =>
    api.post<Building>('/buildings', { body }),
  updateBuilding: (
    id: string,
    body: { name?: string; addressNote?: string; expectedVersion: number },
  ) => api.patch<Building>(idPath('/buildings', id), { body }),
  archiveBuilding: (id: string, archived: boolean, expectedVersion: number) =>
    api.post<Building>(`${idPath('/buildings', id)}/${archived ? 'archive' : 'unarchive'}`, {
      body: { expectedVersion },
    }),
  rooms: (buildingId: string, cursor?: string, signal?: AbortSignal) =>
    api.get<Page<Room>>(`${idPath('/buildings', buildingId)}/rooms`, {
      query: { cursor, limit: 200, includeArchived: true },
      signal,
    }),
  room: (id: string, signal?: AbortSignal) => api.get<Room>(idPath('/rooms', id), { signal }),
  createRoom: (buildingId: string, body: { name: string; floor: string }) =>
    api.post<Room>(`${idPath('/buildings', buildingId)}/rooms`, { body }),
  updateRoom: (id: string, body: { name?: string; floor?: string; expectedVersion: number }) =>
    api.patch<Room>(idPath('/rooms', id), { body }),
  archiveRoom: (id: string, archived: boolean, expectedVersion: number) =>
    api.post<Room>(`${idPath('/rooms', id)}/${archived ? 'archive' : 'unarchive'}`, {
      body: { expectedVersion },
    }),
  racks: (roomId: string, cursor?: string, signal?: AbortSignal) =>
    api.get<Page<Rack>>(`${idPath('/rooms', roomId)}/racks`, {
      query: { cursor, limit: 200, includeArchived: true },
      signal,
    }),
  rack: (id: string, signal?: AbortSignal) => api.get<RackDetail>(idPath('/racks', id), { signal }),
  createRack: (roomId: string, body: { name: string; heightU: number }) =>
    api.post<Rack>(`${idPath('/rooms', roomId)}/racks`, { body }),
  renameRack: (id: string, name: string, expectedVersion: number) =>
    api.patch<Rack>(idPath('/racks', id), { body: { name, expectedVersion } }),
  archiveRack: (id: string, archived: boolean, expectedVersion: number) =>
    api.post<Rack>(`${idPath('/racks', id)}/${archived ? 'archive' : 'unarchive'}`, {
      body: { expectedVersion },
    }),
  placements: (rackId: string, includeRemoved = false, cursor?: string, signal?: AbortSignal) =>
    api.get<Page<Placement>>(`${idPath('/racks', rackId)}/placements`, {
      query: { includeRemoved, cursor, limit: 200 },
      signal,
    }),
  placement: (id: string, signal?: AbortSignal) =>
    api.get<Placement>(idPath('/rack-placements', id), { signal }),
  place: (body: {
    rackId: string;
    assetId: string;
    uPosition: number;
    heightU: number;
    face: string;
  }) => api.post<Placement>('/rack-placements', { body }),
  move: (
    id: string,
    body: {
      rackId: string;
      uPosition: number;
      heightU: number;
      face: string;
      expectedVersion: number;
    },
  ) => api.post<Placement>(`${idPath('/rack-placements', id)}/move`, { body }),
  remove: (id: string, reason: string, expectedVersion: number) =>
    api.post<Placement>(`${idPath('/rack-placements', id)}/remove`, {
      body: { reason, expectedVersion },
    }),
  assetLocation: (id: string, signal?: AbortSignal) =>
    api.get<AssetLocation>(`${idPath('/assets', id)}/location`, { signal }),
  vms: (
    filter: { state?: string; q?: string; hypervisorAssetId?: string },
    cursor?: string,
    signal?: AbortSignal,
  ) => api.get<Page<VM>>('/virtual-machines', { query: { ...filter, cursor, limit: 50 }, signal }),
  vm: (id: string, signal?: AbortSignal) =>
    api.get<VM>(idPath('/virtual-machines', id), { signal }),
  createVM: (body: {
    name: string;
    state: VMState;
    hypervisorAssetId: string | null;
    vcpu: number;
    memoryMb: number;
    managementAddress: string;
    networkNote: string;
    notes: string;
  }) => api.post<VM>('/virtual-machines', { body }),
  updateVM: (
    id: string,
    body: {
      name: string;
      vcpu: number;
      memoryMb: number;
      managementAddress: string;
      networkNote: string;
      notes: string;
      expectedVersion: number;
    },
  ) => api.patch<VM>(idPath('/virtual-machines', id), { body }),
  stateVM: (id: string, state: VMState, expectedVersion: number) =>
    api.post<VM>(`${idPath('/virtual-machines', id)}/state`, { body: { state, expectedVersion } }),
  hypervisorVM: (id: string, assetId: string | null, expectedVersion: number) =>
    api.post<VM>(`${idPath('/virtual-machines', id)}/hypervisor`, {
      body: { assetId, expectedVersion },
    }),
  decommissionVM: (id: string, reason: string, expectedVersion: number) =>
    api.post<VM>(`${idPath('/virtual-machines', id)}/decommission`, {
      body: { reason, expectedVersion },
    }),
};
