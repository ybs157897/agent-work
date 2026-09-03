-- 0031_stream_event_aggregate_version.sql — durable event aggregate version.
--
-- Pre-0031 stream rows did not persist AggregateRef.Version.  Keep those
-- historical rows readable with the explicit compatibility value 0, while
-- requiring every newly appended event to carry the version supplied by the
-- domain event constructor.  The same value is mirrored in the outbox JSON
-- payload so replay and downstream delivery observe one envelope identity.

ALTER TABLE stream_events
    ADD COLUMN aggregate_version INTEGER NOT NULL DEFAULT 0
    CHECK (typeof(aggregate_version) = 'integer' AND aggregate_version >= 0);

-- Existing outbox rows were serialized before the field existed. Backfill
-- the same explicit legacy value so downstream delivery sees the identical
-- envelope identity as SSE replay.
UPDATE outbox_messages
SET payload = json_set(
    payload,
    '$.aggregate_version',
    COALESCE((SELECT aggregate_version FROM stream_events WHERE event_id = outbox_messages.event_id), 0)
)
WHERE json_valid(payload) = 1
  AND json_type(payload, '$.aggregate_version') IS NULL;
