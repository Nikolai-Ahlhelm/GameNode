-- v0.4 normalized Egg container-runtime metadata. Raw Egg JSON is never
-- stored; the bounded normalized plan and compatibility findings are.
ALTER TABLE game_templates ADD COLUMN container_compatibility_json TEXT NOT NULL DEFAULT '';
ALTER TABLE game_templates ADD COLUMN container_runtime_json TEXT NOT NULL DEFAULT '';

-- The existing provisioning job table is extended rather than replaced. The
-- status values remain the established bounded job states; container-specific
-- detail is carried by current_phase and these safe selection fields.
ALTER TABLE provisioning_jobs ADD COLUMN runtime_type TEXT NOT NULL DEFAULT 'native';
ALTER TABLE provisioning_jobs ADD COLUMN selected_image TEXT NOT NULL DEFAULT '';
ALTER TABLE provisioning_jobs ADD COLUMN selected_image_digest TEXT NOT NULL DEFAULT '';

-- Existing container servers can carry a normalized Egg snapshot without
-- widening the closed Engine configuration with arbitrary fields.
ALTER TABLE server_container_configs ADD COLUMN egg_snapshot_json TEXT NOT NULL DEFAULT '';
