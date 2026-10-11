ALTER TABLE requests.request_references
    DROP CONSTRAINT request_references_ref_type_check;
ALTER TABLE requests.request_references
    ADD CONSTRAINT request_references_ref_type_check
    CHECK (ref_type IN ('user', 'product', 'location', 'asset'));
