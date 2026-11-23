-- Schema version 16 (ADR-007 C18.3): a channel of 0.1.x that took 0.1.2's default list, every event there was
-- (status, job_failed, integrity, quarantine, ledger, full_backup, daily_report; schema 11 added the sign-in events to
-- every list), was meant to get everything, so it also gets the category migration, which schema 14 gave no channel.
-- Other channels keep schema 14's mapping. Only migration is added: a category an admin removed since stays removed,
-- and a channel that has migration already is left as it is. Each channel changed is a row in state_changes (entity
-- notification_channel_categories) with what it took before and after, like schema 14's mapping. The server's rule for
-- the deprecated "events" of a new channel is the same (internal/server/notify.go categoriesOfEvents;
-- TestM2EventListsMapToCategories checks that both agree).
CREATE TEMP TABLE c18_all_events AS
SELECT id, categories AS before FROM notification_channels
WHERE (SELECT count(DISTINCT e.value) FROM json_each(notification_channels.m2_events) e
		WHERE e.value IN ('status', 'job_failed', 'integrity', 'quarantine', 'ledger', 'full_backup', 'daily_report')) = 7
	AND NOT EXISTS (SELECT 1 FROM json_each(notification_channels.categories) WHERE value = 'migration');

-- migration is the last category in a channel's list (status, jobs, integrity, security, migration).
UPDATE notification_channels SET categories = json_insert(categories, '$[#]', 'migration'),
	updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id IN (SELECT id FROM c18_all_events);

INSERT INTO state_changes (at, entity, entity_id, from_state, to_state, reason, actor)
SELECT strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), 'notification_channel_categories', c.id,
	json_object('categories', json(a.before), 'daily_report', json(CASE WHEN c.daily_report THEN 'true' ELSE 'false' END)),
	json_object('categories', json(c.categories), 'daily_report', json(CASE WHEN c.daily_report THEN 'true' ELSE 'false' END)),
	'a channel of 0.1.x that took every event of 0.1.2 takes every category, migration included (ADR-007 C18.3): categories ' ||
		a.before || ' -> ' || c.categories,
	'system'
FROM notification_channels c JOIN c18_all_events a ON a.id = c.id ORDER BY c.id;

DROP TABLE c18_all_events;
