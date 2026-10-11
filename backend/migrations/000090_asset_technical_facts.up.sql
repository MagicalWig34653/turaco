-- Asset-owned technical facts. Firmware provenance is recorded with the value;
-- external observation ingestion is a separate adapter concern.
ALTER TABLE assets.assets
    ADD COLUMN firmware_version text CHECK (firmware_version IS NULL OR (length(firmware_version) BETWEEN 1 AND 100 AND firmware_version = btrim(firmware_version))),
    ADD COLUMN firmware_source text CHECK (firmware_source IS NULL OR (length(firmware_source) BETWEEN 1 AND 100 AND firmware_source = btrim(firmware_source))),
    ADD COLUMN firmware_observed_at timestamptz,
    ADD COLUMN last_calibrated_at date,
    ADD COLUMN next_calibration_due date,
    ADD COLUMN next_maintenance_due date,
    ADD CONSTRAINT asset_firmware_provenance CHECK ((firmware_version IS NULL AND firmware_source IS NULL AND firmware_observed_at IS NULL) OR (firmware_version IS NOT NULL AND firmware_source IS NOT NULL AND firmware_observed_at IS NOT NULL)),
    ADD CONSTRAINT asset_calibration_order CHECK (last_calibrated_at IS NULL OR next_calibration_due IS NULL OR next_calibration_due >= last_calibrated_at);
CREATE INDEX assets_calibration_due_idx ON assets.assets (next_calibration_due, id) WHERE next_calibration_due IS NOT NULL;
CREATE INDEX assets_maintenance_due_idx ON assets.assets (next_maintenance_due, id) WHERE next_maintenance_due IS NOT NULL;
