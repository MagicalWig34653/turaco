-- The reference width must never truncate: lpad(..., 6) cuts the sequence number after 999999 and
-- would collide with the unique constraint within one year.
CREATE OR REPLACE FUNCTION requests.next_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'REQ-' || to_char(now(), 'YYYY') || '-'
        || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('requests.request_number_seq') AS n) AS s
$$;

ALTER TABLE requests.service_requests ALTER COLUMN reference SET DEFAULT requests.next_reference();
