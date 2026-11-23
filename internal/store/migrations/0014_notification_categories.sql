-- Schema version 14 (T28, ADR-007 C8, C13, C17 item 4): notification channels subscribe to categories (status, jobs,
-- integrity, security, migration), not to a fixed list of events, so a new event of a category reaches the channels
-- that take the category. The daily report belongs to no category: a channel takes it or not (daily_report).
--
-- A channel added before keeps its event list, as it was, in m2_events (read-only from now on; [] for a channel added
-- later) and gets every category one of its events belongs to, whole: a channel that took quarantine now also gets
-- integrity and ledger. It takes the daily report when its list held daily_report. No event of M2 belongs to the
-- category migration, so no channel gets it here (an admin adds it: PATCH categories). Each channel's change is a
-- row in state_changes (entity notification_channel_categories) with the list before and the categories after.
ALTER TABLE notification_channels RENAME COLUMN events TO m2_events;
ALTER TABLE notification_channels ADD COLUMN categories TEXT NOT NULL DEFAULT '[]';
ALTER TABLE notification_channels ADD COLUMN daily_report INTEGER NOT NULL DEFAULT 0;

-- The categories of the events a channel could take before this version (internal/server/notify.go eventCategory;
-- TestM2EventListsMapToCategories checks that both say the same). ord is the categories' order in a list.
CREATE TEMP TABLE m2_event_category (event TEXT PRIMARY KEY, category TEXT NOT NULL, ord INTEGER NOT NULL);
INSERT INTO m2_event_category (event, category, ord) VALUES
	('status', 'status', 1),
	('job_failed', 'jobs', 2), ('full_backup', 'jobs', 2), ('capacity_short', 'jobs', 2),
	('integrity', 'integrity', 3), ('quarantine', 'integrity', 3), ('ledger', 'integrity', 3),
	('breakglass_signin', 'security', 4), ('new_device_signin', 'security', 4), ('account_locked', 'security', 4);

UPDATE notification_channels SET
	categories = (SELECT json_group_array(category) FROM (
		SELECT DISTINCT m.category, m.ord FROM json_each(notification_channels.m2_events) e
		JOIN m2_event_category m ON m.event = e.value ORDER BY m.ord)),
	daily_report = EXISTS (SELECT 1 FROM json_each(notification_channels.m2_events) WHERE value = 'daily_report');

INSERT INTO state_changes (at, entity, entity_id, from_state, to_state, reason, actor)
SELECT strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), 'notification_channel_categories', id,
	json_object('events', json(m2_events)),
	json_object('categories', json(categories), 'daily_report', json(CASE WHEN daily_report THEN 'true' ELSE 'false' END)),
	'channels take categories since 0.2.0 (ADR-007 C8): events ' || m2_events || ' -> categories ' || categories ||
		CASE WHEN daily_report THEN ' and the daily report' ELSE ', without the daily report' END,
	'system'
FROM notification_channels ORDER BY id;

DROP TABLE m2_event_category;
