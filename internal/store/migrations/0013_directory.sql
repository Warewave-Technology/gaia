-- People and memberships from Active Directory (design 14.2): accounts
-- keyed by objectGUID, their name in the directory, and who closed an
-- account; memberships say whether one is a member or an approver.

-- +goose Up
alter table users add column directory_name text;
alter table users add column disabled_by text;          -- admin | directory
create index users_directory_name on users (provider, directory_name);

alter table team_memberships add column role text not null default 'member';  -- member | approver
alter table team_memberships drop constraint team_memberships_pkey;
alter table team_memberships add primary key (user_id, team, source, role);

-- +goose Down
delete from team_memberships where role <> 'member';
alter table team_memberships drop constraint team_memberships_pkey;
alter table team_memberships add primary key (user_id, team, source);
alter table team_memberships drop column role;
drop index users_directory_name;
alter table users drop column disabled_by;
alter table users drop column directory_name;
