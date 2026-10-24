-- Notifications (design 7.6): every message Gaia sends, once per event
-- and recipient, with its delivery state; and personal subscriptions.

-- +goose Up
alter table users add column email text;

create table user_subscriptions (
    user_id text not null references users (id) on delete cascade,
    trigger text not null,
    primary key (user_id, trigger)
);

create table notifications (
    id           bigint generated always as identity primary key,
    event_key    text        not null,   -- the event: change id and trigger
    trigger      text        not null,
    channel      text        not null,   -- provider name
    recipient    text        not null,   -- team/<name>/<n> or user/<id>
    contract     text,
    change_id    text,
    message      jsonb       not null,   -- the event and the channel's settings, without secrets
    status       text        not null default 'pending',  -- pending | sent | failed
    attempts     int         not null default 0,
    last_error   text,
    created_at   timestamptz not null default now(),
    sent_at      timestamptz,
    unique (event_key, channel, recipient)
);
create index notifications_change on notifications (change_id);

-- +goose Down
drop table notifications;
drop table user_subscriptions;
alter table users drop column email;
