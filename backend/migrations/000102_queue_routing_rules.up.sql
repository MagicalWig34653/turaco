-- Automatic Queue routing for new Tickets (docs/product/f5-service-desk-design.md, "Queue routing rules"): an ordered
-- list of declarative rules; the first enabled rule that matches decides the Queue. No rule assigns people.
CREATE TABLE servicedesk.queue_routing_rules (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    queue_id uuid NOT NULL REFERENCES servicedesk.queues (id),
    position integer NOT NULL CHECK (position >= 0),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    keywords text[] NOT NULL DEFAULT '{}' CHECK (cardinality(keywords) <= 10),
    impact text CHECK (impact IN ('patient_care', 'blocked', 'impaired', 'request')),
    patient_impact boolean,
    enabled boolean NOT NULL DEFAULT true,
    version integer NOT NULL DEFAULT 1,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (cardinality(keywords) > 0 OR impact IS NOT NULL OR patient_impact IS NOT NULL)
);

CREATE INDEX queue_routing_rules_position_idx ON servicedesk.queue_routing_rules (position, id);
