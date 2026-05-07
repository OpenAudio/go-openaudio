-- Add aggregate_user.total_track_count column referenced by handle_track.
-- (apps#0143_add_user_total_tracks introduced this; ported into the same
-- migration as the trigger to keep the dependency local.)

ALTER TABLE aggregate_user ADD COLUMN IF NOT EXISTS total_track_count int DEFAULT 0;

-- ============================================================================
-- handle_user: creates aggregate_user row on user insert. Other triggers
-- (handle_playlist, handle_track, handle_follow) depend on this row existing.
-- Ported verbatim from apps/packages/discovery-provider/ddl/functions/handle_user.sql
-- ============================================================================

CREATE OR REPLACE FUNCTION handle_user() RETURNS TRIGGER AS $$
begin
  insert into aggregate_user (user_id) values (new.user_id) on conflict do nothing;
  return null;
exception
  when others then
    raise warning 'An error occurred in %: %', tg_name, sqlerrm;
    raise;
end;
$$ LANGUAGE plpgsql;

DO $$ BEGIN
  CREATE TRIGGER on_user
  AFTER INSERT ON users
  FOR EACH ROW EXECUTE PROCEDURE handle_user();
EXCEPTION
  WHEN others THEN NULL;
END $$;

-- ============================================================================
-- track_is_public + track_should_notify helpers (used by handle_track)
-- Ported verbatim from apps/packages/discovery-provider/ddl/functions/handle_track.sql
-- ============================================================================

CREATE OR REPLACE FUNCTION track_is_public(track record) RETURNS boolean AS $$
begin
  return track.is_unlisted = false
     and track.is_available = true
     and track.is_delete = false
     and track.stem_of is null;
end
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION track_should_notify(old_track tracks, new_track record, tg_op varchar) RETURNS boolean AS $$
begin
  if tg_op = 'UPDATE' and old_track.track_id is not null then
    return not track_is_public(old_track) and track_is_public(new_track);
  else
    return tg_op = 'INSERT'
      and track_is_public(new_track)
    ;
  end if;
end
$$ LANGUAGE plpgsql;

-- ============================================================================
-- handle_track: maintains aggregate_user.{track_count,total_track_count},
-- aggregate_track row, plus subscriber-create / remix / remix-contest-submission
-- notifications. Ported verbatim.
-- ============================================================================

CREATE OR REPLACE FUNCTION handle_track() RETURNS TRIGGER AS $$
declare
  parent_track_owner_id int;
  subscriber_user_ids int[];
begin
  insert into aggregate_track (track_id) values (new.track_id) on conflict do nothing;
  insert into aggregate_user (user_id) values (new.owner_id) on conflict do nothing;

  update aggregate_user
  set (track_count, total_track_count) = (
    select
      count(*) filter (where t.is_unlisted = false),
      count(*)
    from tracks t
    where t.is_current is true
      and t.is_delete is false
      and t.is_available is true
      and t.stem_of is null
      and t.owner_id = new.owner_id
  )
  where user_id = new.owner_id;

  begin
    if track_should_notify(OLD, new, TG_OP) AND new.is_playlist_upload = FALSE THEN
      select array(
        select subscriber_id
          from subscriptions
          where is_current
            and not is_delete
            and user_id = new.owner_id
      ) into subscriber_user_ids;

      if array_length(subscriber_user_ids, 1) > 0 then
        insert into notification
          (blocknumber, user_ids, timestamp, type, specifier, group_id, data)
        values
          (
            new.blocknumber,
            subscriber_user_ids,
            new.updated_at,
            'create',
            new.track_id,
            'create:track:user_id:' || new.owner_id,
            json_build_object('track_id', new.track_id)
          )
        on conflict do nothing;
      end if;
    end if;
  exception
    when others then
      raise warning 'An error occurred in %: %', tg_name, sqlerrm;
  end;

  begin
    if track_should_notify(OLD, new, TG_OP) AND new.remix_of is not null THEN
      select owner_id into parent_track_owner_id from tracks
      where is_current and track_id = (new.remix_of->'tracks'->0->>'parent_track_id')::int
      limit 1;
      if parent_track_owner_id is not null then
        insert into notification
          (blocknumber, user_ids, timestamp, type, specifier, group_id, data)
        values
          (
            new.blocknumber,
            ARRAY [parent_track_owner_id],
            new.updated_at,
            'remix',
            new.owner_id,
            'remix:track:' || new.track_id || ':parent_track:' || (new.remix_of->'tracks'->0->>'parent_track_id')::int,
            json_build_object('track_id', new.track_id, 'parent_track_id', (new.remix_of->'tracks'->0->>'parent_track_id')::int)
          )
        on conflict do nothing;
      end if;
    end if;
  exception
    when others then
      raise warning 'An error occurred in %: %', tg_name, sqlerrm;
  end;

  begin
    if track_should_notify(OLD, new, TG_OP) AND new.remix_of is not null THEN
      declare
        contest_event_id int;
        contest_creator_id int;
        submission_count int;
        milestone int;
        parent_track_id int := (new.remix_of->'tracks'->0->>'parent_track_id')::int;
      begin
        select event_id, user_id
        into contest_event_id, contest_creator_id
        from events
        where event_type = 'remix_contest'
          and is_deleted = false
          and end_date > now()
          and entity_id = parent_track_id
        limit 1;

        if contest_event_id is not null then
          select count(*) into submission_count
          from tracks t
          join events e on e.event_type = 'remix_contest'
            and e.is_deleted = false
            and e.entity_id = parent_track_id
          where t.is_current = true
            and t.is_delete = false
            and t.remix_of is not null
            and (t.remix_of->'tracks'->0->>'parent_track_id')::int = parent_track_id
            and t.created_at >= e.created_at;

          FOREACH milestone IN ARRAY ARRAY[1, 10, 50] LOOP
            IF submission_count = milestone THEN
              insert into notification
                (blocknumber, user_ids, timestamp, type, specifier, group_id, data)
              values
                (
                  new.blocknumber,
                  ARRAY [contest_creator_id],
                  new.updated_at,
                  'artist_remix_contest_submissions',
                  milestone || ':' || contest_event_id,
                  'artist_remix_contest_submissions:' || contest_event_id || ':' || milestone,
                  json_build_object(
                    'event_id', contest_event_id,
                    'milestone', milestone,
                    'entity_id', parent_track_id
                  )
                )
              on conflict do nothing;
            END IF;
          END LOOP;
        end if;
      end;
    end if;
  exception
    when others then
      raise warning 'An error occurred in %: %', tg_name, sqlerrm;
  end;

  return null;

exception
  when others then
    raise warning 'An error occurred in %: %', tg_name, sqlerrm;
    raise;
end;
$$ LANGUAGE plpgsql;

DO $$ BEGIN
  CREATE TRIGGER on_track
  AFTER INSERT OR UPDATE ON tracks
  FOR EACH ROW EXECUTE PROCEDURE handle_track();
EXCEPTION
  WHEN others THEN NULL;
END $$;

-- ============================================================================
-- handle_playlist: maintains aggregate_playlist row, aggregate_user.{playlist_count,
-- album_count}, plus subscriber-create and track-added-to-playlist notifications.
--
-- The apps version reads previous state from `revert_blocks` (apps' reorg
-- table) to compute delta. go-openaudio doesn't track reverts, so we strip
-- that lookup. With old_row left null:
--   - insert of public playlist (is_private=false) → delta=1 (correct)
--   - insert of private playlist → delta=0 (correct)
-- The private→public publish path in apps fires this trigger via INSERT of
-- a new is_current row; go-openaudio's playlist_update.go does in-place UPDATE
-- so this trigger won't fire on publish today. That gap is tracked separately
-- (see Stack 4A publish_scheduled_releases / Stack 2 in-block updates).
-- ============================================================================

CREATE OR REPLACE FUNCTION handle_playlist() RETURNS TRIGGER AS $$
declare
  track_owner_id int := 0;
  track_item jsonb;
  subscriber_user_ids integer[];
  delta int := 0;
begin
  insert into aggregate_playlist (playlist_id, is_album) values (new.playlist_id, new.is_album) on conflict do nothing;

  if new.is_private = false then
    delta := 1;
  end if;

  if delta != 0 then
    if new.is_album then
      update aggregate_user
      set album_count = album_count + delta
      where user_id = new.playlist_owner_id;
    else
      update aggregate_user
      set playlist_count = playlist_count + delta
      where user_id = new.playlist_owner_id;
    end if;
  end if;

  begin
    if new.is_private = false and new.is_delete = false and new.created_at = new.updated_at then
      select array(
        select subscriber_id
          from subscriptions
          where is_current
            and not is_delete
            and user_id = new.playlist_owner_id
      ) into subscriber_user_ids;
      if array_length(subscriber_user_ids, 1) > 0 then
        insert into notification
          (blocknumber, user_ids, timestamp, type, specifier, group_id, data)
        values
          (
            new.blocknumber,
            subscriber_user_ids,
            new.updated_at,
            'create',
            new.playlist_owner_id,
            'create:playlist_id:' || new.playlist_id,
            json_build_object('playlist_id', new.playlist_id, 'is_album', new.is_album)
          )
        on conflict do nothing;
      end if;
    end if;
  exception
    when others then
      raise warning 'An error occurred in %: %', tg_name, sqlerrm;
  end;

  begin
    if new.is_delete is false and new.is_private is false then
      for track_item IN select jsonb_array_elements from jsonb_array_elements(new.playlist_contents->'track_ids')
      loop
        if (track_item->>'time')::double precision::int >= extract(epoch from new.updated_at)::int then
          select owner_id into track_owner_id from tracks where is_current and track_id = (track_item->>'track')::int;
          if track_owner_id != new.playlist_owner_id then
            insert into notification
              (blocknumber, user_ids, timestamp, type, specifier, group_id, data)
            values
              (
                new.blocknumber,
                ARRAY [track_owner_id],
                new.updated_at,
                'track_added_to_playlist',
                track_owner_id,
                'track_added_to_playlist:playlist_id:' || new.playlist_id || ':track_id:' || (track_item->>'track')::int,
                json_build_object('track_id', (track_item->>'track')::int, 'playlist_id', new.playlist_id, 'playlist_owner_id', new.playlist_owner_id)
              )
            on conflict do nothing;
          end if;
        end if;
      end loop;
    end if;
  exception
    when others then
      raise warning 'An error occurred in %: %', tg_name, sqlerrm;
  end;

  return null;

exception
  when others then
    raise warning 'An error occurred in %: %', tg_name, sqlerrm;
    raise;
end;
$$ LANGUAGE plpgsql;

DO $$ BEGIN
  CREATE TRIGGER on_playlist
  AFTER INSERT ON playlists
  FOR EACH ROW EXECUTE PROCEDURE handle_playlist();
EXCEPTION
  WHEN others THEN NULL;
END $$;
