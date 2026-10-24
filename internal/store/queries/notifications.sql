-- name: InsertNotification :one
-- Nothing is inserted for an event already sent to this recipient.
insert into notifications (event_key, trigger, channel, recipient, contract, change_id, message)
values ($1, $2, $3, $4, $5, $6, $7)
on conflict (event_key, channel, recipient) do nothing
returning id;

-- name: Notification :one
select * from notifications where id = $1;

-- name: NotificationSent :exec
update notifications set status = 'sent', attempts = attempts + 1, last_error = null, sent_at = now() where id = $1;

-- name: NotificationFailed :exec
update notifications set status = $2, attempts = attempts + 1, last_error = $3 where id = $1;

-- name: Notifications :many
select * from notifications
where (sqlc.arg(change_id)::text = '' or change_id = sqlc.arg(change_id)::text)
order by id desc limit sqlc.arg(max_rows);

-- name: Subscribers :many
-- Enabled people with an address who subscribed to trigger.
select u.id, u.username, u.email from user_subscriptions s join users u on u.id = s.user_id
where s.trigger = $1 and u.enabled and u.email is not null and u.email <> '' order by u.username;

-- name: Subscriptions :many
select trigger from user_subscriptions where user_id = $1 order by trigger;

-- name: ClearSubscriptions :exec
delete from user_subscriptions where user_id = $1;

-- name: AddSubscription :exec
insert into user_subscriptions (user_id, trigger) values ($1, $2) on conflict do nothing;

-- name: SetEmail :exec
update users set email = $2 where id = $1;
