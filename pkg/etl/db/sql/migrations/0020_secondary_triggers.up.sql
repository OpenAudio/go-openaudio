-- Add aggregate columns referenced by handle_comment + handle_share.
-- (apps#0145 added share_count + track_share_count; comment_count came in
-- a separate apps migration. Ported into one place here.)

ALTER TABLE aggregate_track ADD COLUMN IF NOT EXISTS comment_count int NOT NULL DEFAULT 0;
ALTER TABLE aggregate_track ADD COLUMN IF NOT EXISTS share_count int DEFAULT 0;
ALTER TABLE aggregate_playlist ADD COLUMN IF NOT EXISTS share_count int DEFAULT 0;
ALTER TABLE aggregate_user ADD COLUMN IF NOT EXISTS track_share_count int DEFAULT 0;

-- ============================================================================
-- handle_comment: maintains aggregate_track.comment_count.
-- Ported verbatim from apps/packages/discovery-provider/ddl/functions/handle_comment.sql
-- ============================================================================

CREATE OR REPLACE FUNCTION handle_comment() RETURNS TRIGGER AS $$
begin
  if new.entity_type = 'Track' then
    insert into aggregate_track (track_id) values (new.entity_id) on conflict do nothing;
  end if;

  if new.entity_type = 'Track' then
    update aggregate_track
    set comment_count = (
      select count(*)
      from comments c
      where c.is_delete is false
        and c.is_visible is true
        and c.entity_type = new.entity_type
        and c.entity_id = new.entity_id
    )
    where track_id = new.entity_id;
  end if;

  return null;
exception
  when others then
    raise warning 'An error occurred in %: %', tg_name, sqlerrm;
    return null;
end;
$$ LANGUAGE plpgsql;

DO $$ BEGIN
  CREATE TRIGGER on_comment
  AFTER INSERT ON comments
  FOR EACH ROW EXECUTE PROCEDURE handle_comment();
EXCEPTION
  WHEN others THEN NULL;
END $$;

-- ============================================================================
-- handle_event: creates fan_remix_contest_started notifications when a remix
-- contest event is created on a public track. Ported verbatim.
-- ============================================================================

CREATE OR REPLACE FUNCTION handle_event() RETURNS TRIGGER AS $$
declare
  notified_user_id int;
  owner_user_id int;
  is_track_public boolean;
begin
  if new.event_type = 'remix_contest' and new.is_deleted = false then
    select owner_id, not is_unlisted into owner_user_id, is_track_public
    from tracks
    where is_current and track_id = new.entity_id
    limit 1;

    if is_track_public then
      for notified_user_id in
        select distinct user_id
        from (
          select f.follower_user_id as user_id
          from follows f
          where f.followee_user_id = new.user_id
            and f.is_current = true
            and f.is_delete = false
          union
          select s.user_id
          from saves s
          where s.save_item_id = new.entity_id
            and s.save_type = 'track'
            and s.is_current = true
            and s.is_delete = false
        ) as users_to_notify
      loop
        insert into notification
          (blocknumber, user_ids, timestamp, type, specifier, group_id, data)
        values
          (
            new.blocknumber,
            ARRAY[notified_user_id],
            new.created_at,
            'fan_remix_contest_started',
            notified_user_id,
            'fan_remix_contest_started:' || new.entity_id || ':user:' || new.user_id,
            json_build_object('entity_user_id', owner_user_id, 'entity_id', new.entity_id)
          )
        on conflict do nothing;
      end loop;
    end if;
  end if;

  return null;
exception
  when others then
    raise warning 'An error occurred in %: %', tg_name, sqlerrm;
    return null;
end;
$$ LANGUAGE plpgsql;

DO $$ BEGIN
  CREATE TRIGGER on_event
  AFTER INSERT ON events
  FOR EACH ROW EXECUTE PROCEDURE handle_event();
EXCEPTION
  WHEN others THEN NULL;
END $$;

-- ============================================================================
-- handle_share: maintains aggregate_track.share_count or aggregate_playlist.share_count,
-- and aggregate_user.track_share_count for track shares.
-- Ported verbatim from apps/packages/discovery-provider/ddl/functions/handle_share.sql
-- ============================================================================

CREATE OR REPLACE FUNCTION handle_share() RETURNS TRIGGER AS $$
begin
  insert into aggregate_user (user_id) values (new.user_id) on conflict do nothing;

  if new.share_type::text = 'track' then
    insert into aggregate_track (track_id) values (new.share_item_id) on conflict do nothing;
  else
    insert into aggregate_playlist (playlist_id, is_album)
    select p.playlist_id, p.is_album
    from playlists p
    where p.playlist_id = new.share_item_id
      and p.is_current
    on conflict do nothing;
  end if;

  if new.share_type::text = 'track' then
    update aggregate_track
    set share_count = (
      select count(*)
      from shares s
      where s.share_type = new.share_type
        and s.share_item_id = new.share_item_id
    )
    where track_id = new.share_item_id;

    update aggregate_user
    set track_share_count = (
      select count(*)
      from shares s
      where s.user_id = new.user_id
        and s.share_type = new.share_type
    )
    where user_id = new.user_id;
  else
    update aggregate_playlist
    set share_count = (
      select count(*)
      from shares s
      where s.share_type = new.share_type
        and s.share_item_id = new.share_item_id
    )
    where playlist_id = new.share_item_id;
  end if;

  return null;
exception
  when others then
    raise warning 'An error occurred in %: %', tg_name, sqlerrm;
    return null;
end;
$$ LANGUAGE plpgsql;

DO $$ BEGIN
  CREATE TRIGGER on_share
  AFTER INSERT ON shares
  FOR EACH ROW EXECUTE PROCEDURE handle_share();
EXCEPTION
  WHEN others THEN NULL;
END $$;

-- handle_supporter_rank_up intentionally not ported: it depends on
-- user_bank_txs and supporter_rank_ups tables which are part of the Solana
-- indexer (out of scope per parity plan).
