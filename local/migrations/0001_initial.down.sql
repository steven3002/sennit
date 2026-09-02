-- Reverses 0001 by dropping every object it creates.
--
-- ⚠️ This destroys the device's working copy: bodies, ranking metadata, the
-- lexical index, the read-location caches, the pending write queue and the
-- session heads. Records already on the network survive and can be hydrated
-- back; anything still sitting in the queue has not reached the network and is
-- lost with the table. Nothing in the shipped product calls this. It exists so
-- that the migration sequence is reversible and so that the runner's stepping
-- can be tested against a real database rather than asserted.
--
-- The two DROP TABLE statements in the up direction are not reversed. They
-- remove `vectors` and `slab_meta`, whose contents were superseded rather than
-- moved; recreating either as an empty table would be a worse lie than leaving
-- it absent.

DROP TABLE IF EXISTS meta;

DROP INDEX IF EXISTS session_heads_by_parent;
DROP INDEX IF EXISTS session_heads_by_updated;
DROP TABLE IF EXISTS session_heads;

DROP INDEX IF EXISTS queue_by_seq;
DROP TABLE IF EXISTS queue;

DROP TABLE IF EXISTS slabs;
DROP TABLE IF EXISTS read_stats;
DROP TABLE IF EXISTS object_cache;
DROP TABLE IF EXISTS slab_cache;

DROP TABLE IF EXISTS record_lexical;

DROP INDEX IF EXISTS record_tags_by_tag;
DROP TABLE IF EXISTS record_tags;

DROP INDEX IF EXISTS record_meta_supersedes;
DROP TABLE IF EXISTS record_meta;

DROP TABLE IF EXISTS bodies;
