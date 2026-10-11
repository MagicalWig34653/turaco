import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'organization.sponsor_invalid': 'external.error.sponsorInvalid',
  'organization.party_inactive': 'external.error.partyInactive',
  'organization.party_name_taken': 'external.error.nameTaken',
});

export type PartyKind = 'vendor' | 'contractor' | 'partner';
export const partyKinds: readonly PartyKind[] = ['vendor', 'contractor', 'partner'];

/** An External Party: the company or person group external accounts belong to. */
export type ExternalParty = {
  id: string;
  kind: PartyKind;
  name: string;
  sponsorUserId: string;
  sponsorName: string;
  active: boolean;
  notes: string;
  version: number;
  accountCount: number;
  createdAt: string;
  updatedAt: string;
};

export type PartyAccount = {
  userId: string;
  displayName: string;
  status: string;
  accessExpiresAt: string;
};

export type ExternalPartyDetail = ExternalParty & { accounts: PartyAccount[] };

export type ExternalPartyCreate = {
  kind: PartyKind;
  name: string;
  sponsorUserId: string;
  notes?: string;
};

export type ExternalPartyUpdate = {
  expectedVersion: number;
  kind?: PartyKind;
  name?: string;
  sponsorUserId?: string;
  notes?: string;
  active?: boolean;
};

/** External Parties (F14 slice A-G); every call needs organization.external_parties.manage. */
export const externalPartiesApi = {
  list: (signal?: Signal) =>
    api.get<{ items: ExternalParty[]; kinds: PartyKind[] }>('/external-parties', { signal }),
  get: (id: string, signal?: Signal) =>
    api.get<ExternalPartyDetail>(`/external-parties/${enc(id)}`, { signal }),
  create: (body: ExternalPartyCreate) => api.post<ExternalParty>('/external-parties', body),
  update: (id: string, body: ExternalPartyUpdate) =>
    api.patch<ExternalParty>(`/external-parties/${enc(id)}`, body),
};
