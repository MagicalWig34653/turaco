-- External Parties and ticket participants (F14 slice A-G, ADR-0034 point 4).
--
-- An External Party is the company or person group an external account belongs to. It names an internal sponsor
-- who is responsible for the accounts and is notified before access ends. The party record carries no address or
-- contract data. External accounts (organization.users.account_kind = 'external', migration 000065) reference it.

CREATE TABLE IF NOT EXISTS organization.external_parties (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    kind text NOT NULL CHECK (kind IN ('vendor', 'contractor', 'partner')),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 200),
    sponsor_user_id uuid NOT NULL REFERENCES organization.users(id),
    active boolean NOT NULL DEFAULT true,
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 500),
    version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- A party name is unique among all parties so two lists never show the same company twice.
CREATE UNIQUE INDEX IF NOT EXISTS external_parties_name_idx ON organization.external_parties (lower(name));
CREATE INDEX IF NOT EXISTS external_parties_sponsor_idx ON organization.external_parties (sponsor_user_id);

ALTER TABLE organization.users
    ADD CONSTRAINT users_external_party_fk FOREIGN KEY (external_party_id) REFERENCES organization.external_parties(id),
    ADD CONSTRAINT users_external_needs_party CHECK (account_kind = 'employee' OR external_party_id IS NOT NULL);

CREATE INDEX IF NOT EXISTS users_external_party_idx ON organization.users (external_party_id) WHERE external_party_id IS NOT NULL;
-- The expiry job and the reminders scan active external accounts by their end of access.
CREATE INDEX IF NOT EXISTS users_access_expiry_idx ON organization.users (access_expires_at)
    WHERE account_kind = 'external' AND status = 'active';

-- Ticket participants (owned by Service Desk): the only way an external account sees a Ticket. user_id is a plain
-- uuid like every other user reference of this module; the service checks that it names an active external account.
CREATE TABLE IF NOT EXISTS servicedesk.ticket_participants (
    ticket_id uuid NOT NULL REFERENCES servicedesk.tickets(id) ON DELETE CASCADE,
    user_id uuid NOT NULL,
    level text NOT NULL CHECK (level IN ('read', 'comment')),
    added_by uuid NOT NULL,
    added_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz,
    PRIMARY KEY (ticket_id, user_id)
);

CREATE INDEX IF NOT EXISTS ticket_participants_user_idx ON servicedesk.ticket_participants (user_id, ticket_id);
