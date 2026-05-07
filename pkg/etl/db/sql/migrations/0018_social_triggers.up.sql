-- Add aggregate_user.score column referenced by trigger shadowban checks.
-- Defaults to 0 (not shadowbanned). The score-computation pipeline isn't
-- ported yet; treating everyone as not-shadowbanned matches the bootstrap
-- state in apps before scores are computed.

ALTER TABLE aggregate_user ADD COLUMN IF NOT EXISTS score double precision NOT NULL DEFAULT 0;

-- ============================================================================
-- handle_follow: maintain aggregate_user.{follower_count,following_count},
-- milestone insert at thresholds, follower-count and follow notifications.
-- Ported verbatim from apps/packages/discovery-provider/ddl/functions/handle_follow.sql
-- ============================================================================

CREATE OR REPLACE FUNCTION handle_follow() RETURNS TRIGGER AS $$
declare
  new_follower_count int;
  milestone integer;
  delta int;
  is_shadowbanned boolean;
begin
  insert into aggregate_user (user_id) values (new.followee_user_id) on conflict do nothing;
  insert into aggregate_user (user_id) values (new.follower_user_id) on conflict do nothing;

  if new.is_delete then
    delta := -1;
  else
    delta := 1;
  end if;

  update aggregate_user
  set following_count = following_count + delta
  where user_id = new.follower_user_id;

  update aggregate_user
  set follower_count = follower_count + delta
  where user_id = new.followee_user_id
  returning follower_count into new_follower_count;

  select new_follower_count into milestone where new_follower_count in (10, 25, 50, 100, 250, 500, 1000, 5000, 10000, 20000, 50000, 100000, 1000000);
  select score < 0 into is_shadowbanned from aggregate_user where user_id = new.follower_user_id;
  if milestone is not null and new.is_delete is false and is_shadowbanned = false then
    insert into milestones
      (id, name, threshold, blocknumber, slot, timestamp)
    values
      (new.followee_user_id, 'FOLLOWER_COUNT', milestone, new.blocknumber, new.slot, new.created_at)
    on conflict do nothing;
    insert into notification
      (user_ids, type, group_id, specifier, blocknumber, timestamp, data)
      values
      (
        ARRAY [new.followee_user_id],
        'milestone_follower_count',
        'milestone:FOLLOWER_COUNT:id:' || new.followee_user_id || ':threshold:' || milestone,
        new.followee_user_id,
        new.blocknumber,
        new.created_at,
        json_build_object('type', 'FOLLOWER_COUNT', 'user_id', new.followee_user_id, 'threshold', milestone)
      )
    on conflict do nothing;
  end if;

  begin
    if new.is_delete is false and is_shadowbanned = false then
      insert into notification
      (blocknumber, user_ids, timestamp, type, specifier, group_id, data)
      values
      (
        new.blocknumber,
        ARRAY [new.followee_user_id],
        new.created_at,
        'follow',
        new.follower_user_id,
        'follow:' || new.followee_user_id,
        json_build_object('followee_user_id', new.followee_user_id, 'follower_user_id', new.follower_user_id)
      )
      on conflict do nothing;
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
  CREATE TRIGGER on_follow
  AFTER INSERT ON follows
  FOR EACH ROW EXECUTE PROCEDURE handle_follow();
EXCEPTION
  WHEN others THEN NULL;
END $$;

-- ============================================================================
-- handle_save: maintain aggregate_track.save_count or aggregate_playlist.save_count,
-- aggregate_user.track_save_count, milestone + save + cosign + save-of-repost notifications.
--
-- Ported from apps with two stubs:
--  - is_purchased / is_containing_album_purchased default to false.
--    The original queries reference usdc_purchases (Solana-side) and
--    playlist_tracks (Stack 2A). In this environment without Solana
--    indexing nothing is purchased via USDC, so false is correct.
-- ============================================================================

CREATE OR REPLACE FUNCTION handle_save() RETURNS TRIGGER AS $$
declare
  new_val int;
  milestone_name text;
  milestone integer;
  owner_user_id int;
  track_remix_of jsonb;
  is_remix_cosign boolean;
  is_album boolean;
  delta int;
  entity_type text;
  is_purchased boolean default false;
  is_containing_album_purchased boolean default false;
  is_shadowbanned boolean;
begin
  insert into aggregate_user (user_id) values (new.user_id) on conflict do nothing;
  if new.save_type::text = 'track' then
    insert into aggregate_track (track_id) values (new.save_item_id) on conflict do nothing;
    entity_type := 'track';
    -- usdc_purchases / playlist_tracks not present in go-openaudio env;
    -- without Solana indexing nothing is purchased so is_purchased remains false.
  else
    insert into aggregate_playlist (playlist_id, is_album)
    select p.playlist_id, p.is_album
    from playlists p
    where p.playlist_id = new.save_item_id
      and p.is_current
    on conflict do nothing;

    select ap.is_album into is_album
    from aggregate_playlist ap
    where ap.playlist_id = new.save_item_id;
    -- usdc_purchases not present; is_purchased remains false.
  end if;

  if new.is_delete then
    delta := -1;
  else
    delta := 1;
  end if;

  if new.save_type::text = 'track' then
    milestone_name := 'TRACK_SAVE_COUNT';

    update aggregate_track
    set save_count = (
      select count(*)
      from saves r
      where r.is_current is true
        and r.is_delete is false
        and r.save_type = new.save_type
        and r.save_item_id = new.save_item_id
    )
    where track_id = new.save_item_id
    returning save_count into new_val;

    update aggregate_user
    set track_save_count = (
      select count(*)
      from saves r
      where r.is_current is true
        and r.is_delete is false
        and r.user_id = new.user_id
        and r.save_type = new.save_type
    )
    where user_id = new.user_id;

    if new.is_delete is false then
      select tracks.owner_id, tracks.remix_of into owner_user_id, track_remix_of
      from tracks where is_current and track_id = new.save_item_id;
    end if;
  else
    milestone_name := 'PLAYLIST_SAVE_COUNT';

    update aggregate_playlist
    set save_count = (
      select count(*)
      from saves r
      where r.is_current is true
        and r.is_delete is false
        and r.save_type = new.save_type
        and r.save_item_id = new.save_item_id
    )
    where playlist_id = new.save_item_id
    returning save_count into new_val;

    if new.is_delete is false then
      select playlists.playlist_owner_id into owner_user_id from playlists where is_current and playlist_id = new.save_item_id;
    end if;
  end if;

  select new_val into milestone where new_val in (10,25,50,100,250,500,1000,2500,5000,10000,25000,50000,100000,250000,500000,1000000);
  select score < 0 into is_shadowbanned from aggregate_user where user_id = new.user_id;

  if new.is_delete = false and milestone is not null and is_shadowbanned = false then
    insert into milestones
      (id, name, threshold, blocknumber, slot, timestamp)
    values
      (new.save_item_id, milestone_name, milestone, new.blocknumber, new.slot, new.created_at)
    on conflict do nothing;

    if entity_type = 'track' then
      insert into notification
        (user_ids, type, specifier, group_id, blocknumber, timestamp, data)
        values
        (
          ARRAY [owner_user_id],
          'milestone',
          owner_user_id,
          'milestone:' || milestone_name  || ':id:' || new.save_item_id || ':threshold:' || milestone,
          new.blocknumber,
          new.created_at,
          json_build_object('type', milestone_name, 'track_id', new.save_item_id, 'threshold', milestone)
        )
        on conflict do nothing;
    else
      insert into notification
        (user_ids, type, specifier, group_id, blocknumber, timestamp, data)
        values
        (
          ARRAY [owner_user_id],
          'milestone',
          owner_user_id,
          'milestone:' || milestone_name  || ':id:' || new.save_item_id || ':threshold:' || milestone,
          new.blocknumber,
          new.created_at,
          json_build_object('type', milestone_name, 'playlist_id', new.save_item_id, 'threshold', milestone, 'is_album', is_album)
        )
        on conflict do nothing;
    end if;
  end if;

  begin
    if new.is_delete is false and is_purchased is false and is_shadowbanned = false then
      insert into notification
        (blocknumber, user_ids, timestamp, type, specifier, group_id, data)
        values
        (
          new.blocknumber,
          ARRAY [owner_user_id],
          new.created_at,
          'save',
          new.user_id,
          'save:' || new.save_item_id || ':type:' || new.save_type,
          json_build_object('save_item_id', new.save_item_id, 'user_id', new.user_id, 'type', new.save_type)
        )
      on conflict do nothing;
    end if;

    if new.is_delete is false
       and new.is_save_of_repost is true
       and is_shadowbanned = false
       and is_containing_album_purchased is false then
      with
        followee_save_repost_ids as (
          select user_id
          from reposts r
          where r.repost_item_id = new.save_item_id
            and new.created_at - INTERVAL '1 month' < r.created_at
            and new.created_at > r.created_at
            and r.is_delete is false
            and r.is_current is true
            and r.repost_type::text = new.save_type::text
            and r.user_id in (
              select followee_user_id
              from follows
              where follower_user_id = new.user_id
                and is_delete is false
                and is_current is true
            )
        )
      insert into notification
        (blocknumber, user_ids, timestamp, type, specifier, group_id, data)
      SELECT blocknumber_val, user_ids_val, timestamp_val, type_val, specifier_val, group_id_val, data_val
      FROM (
        SELECT new.blocknumber AS blocknumber_val,
        ARRAY(
          SELECT user_id FROM followee_save_repost_ids
        ) AS user_ids_val,
        new.created_at AS timestamp_val,
        'save_of_repost' AS type_val,
        new.user_id AS specifier_val,
        'save_of_repost:' || new.save_item_id || ':type:' || new.save_type AS group_id_val,
        json_build_object(
          'save_of_repost_item_id', new.save_item_id,
          'user_id', new.user_id,
          'type', case when is_album then 'album' else new.save_type::text end
        ) AS data_val
      ) sub
      WHERE user_ids_val IS NOT NULL AND array_length(user_ids_val, 1) > 0
      on conflict do nothing;
    end if;

    if new.is_delete is false and new.save_type::text = 'track' and track_remix_of is not null and is_shadowbanned = false then
      select case when tracks.owner_id = new.user_id then TRUE else FALSE end into is_remix_cosign
        from tracks
        where is_current and track_id = (track_remix_of->'tracks'->0->>'parent_track_id')::int;
      if is_remix_cosign then
        insert into notification
          (blocknumber, user_ids, timestamp, type, specifier, group_id, data)
          values
          (
            new.blocknumber,
            ARRAY [owner_user_id],
            new.created_at,
            'cosign',
            new.user_id,
            'cosign:parent_track' || (track_remix_of->'tracks'->0->>'parent_track_id')::int || ':original_track:' || new.save_item_id,
            json_build_object('parent_track_id', (track_remix_of->'tracks'->0->>'parent_track_id')::int, 'track_id', new.save_item_id, 'track_owner_id', owner_user_id)
          )
        on conflict do nothing;
      end if;
    end if;
  exception
    when others then
      raise warning 'An error occurred in %: %', tg_name, sqlerrm;
      return null;
  end;

  return null;

exception
  when others then
    raise warning 'An error occurred in %: %', tg_name, sqlerrm;
    raise;
end;
$$ LANGUAGE plpgsql;

DO $$ BEGIN
  CREATE TRIGGER on_save
  AFTER INSERT ON saves
  FOR EACH ROW EXECUTE PROCEDURE handle_save();
EXCEPTION
  WHEN others THEN NULL;
END $$;

-- ============================================================================
-- handle_repost: maintain aggregate_track.repost_count / aggregate_playlist.repost_count,
-- aggregate_user.repost_count, milestone + repost + repost-of-repost + cosign notifications.
-- Ported verbatim from apps/packages/discovery-provider/ddl/functions/handle_repost.sql.
-- ============================================================================

CREATE OR REPLACE FUNCTION handle_repost() RETURNS TRIGGER AS $$
declare
  new_val int;
  milestone_name text;
  milestone integer;
  owner_user_id int;
  track_remix_of jsonb;
  is_remix_cosign boolean;
  is_album boolean;
  delta int;
  entity_type text;
  is_shadowbanned boolean;
begin
  insert into aggregate_user (user_id) values (new.user_id) on conflict do nothing;
  if new.repost_type::text = 'track' then
    insert into aggregate_track (track_id) values (new.repost_item_id) on conflict do nothing;
    entity_type := 'track';
  else
    insert into aggregate_playlist (playlist_id, is_album)
    select p.playlist_id, p.is_album
    from playlists p
    where p.playlist_id = new.repost_item_id
      and p.is_current
    on conflict do nothing;

    entity_type := 'playlist';

    select ap.is_album into is_album
    from aggregate_playlist ap
    where ap.playlist_id = new.repost_item_id;
  end if;

  if new.is_delete then
    delta := -1;
  else
    delta := 1;
  end if;

  update aggregate_user
  set repost_count = (
    select count(*)
    from reposts r
    where r.is_current is true
      and r.is_delete is false
      and r.user_id = new.user_id
  )
  where user_id = new.user_id;

  if new.repost_type::text = 'track' then
    milestone_name := 'TRACK_REPOST_COUNT';
    update aggregate_track
    set repost_count = (
      select count(*)
      from reposts r
      where r.is_current is true
        and r.is_delete is false
        and r.repost_type = new.repost_type
        and r.repost_item_id = new.repost_item_id
    )
    where track_id = new.repost_item_id
    returning repost_count into new_val;

    if new.is_delete is false then
      select tracks.owner_id, tracks.remix_of into owner_user_id, track_remix_of
      from tracks where is_current and track_id = new.repost_item_id;
    end if;
  else
    milestone_name := 'PLAYLIST_REPOST_COUNT';
    update aggregate_playlist
    set repost_count = (
      select count(*)
      from reposts r
      where r.is_current is true
        and r.is_delete is false
        and r.repost_type = new.repost_type
        and r.repost_item_id = new.repost_item_id
    )
    where playlist_id = new.repost_item_id
    returning repost_count into new_val;

    if new.is_delete is false then
      select playlist_owner_id into owner_user_id from playlists where is_current and playlist_id = new.repost_item_id;
    end if;
  end if;

  select new_val into milestone where new_val in (10,25,50,100,250,500,1000,2500,5000,10000,25000,50000,100000,250000,500000,1000000);
  select score < 0 into is_shadowbanned from aggregate_user where user_id = new.user_id;

  if new.is_delete = false and milestone is not null and owner_user_id is not null and is_shadowbanned = false then
    insert into milestones
      (id, name, threshold, blocknumber, slot, timestamp)
    values
      (new.repost_item_id, milestone_name, milestone, new.blocknumber, new.slot, new.created_at)
    on conflict do nothing;

    if entity_type = 'track' then
      insert into notification
        (user_ids, type, specifier, group_id, blocknumber, timestamp, data)
        values
        (
          ARRAY [owner_user_id],
          'milestone',
          owner_user_id,
          'milestone:' || milestone_name  || ':id:' || new.repost_item_id || ':threshold:' || milestone,
          new.blocknumber,
          new.created_at,
          json_build_object('type', milestone_name, 'track_id', new.repost_item_id, 'threshold', milestone)
        )
        on conflict do nothing;
    else
      insert into notification
        (user_ids, type, specifier, group_id, blocknumber, timestamp, data)
        values
        (
          ARRAY [owner_user_id],
          'milestone',
          owner_user_id,
          'milestone:' || milestone_name  || ':id:' || new.repost_item_id || ':threshold:' || milestone,
          new.blocknumber,
          new.created_at,
          json_build_object('type', milestone_name, 'playlist_id', new.repost_item_id, 'threshold', milestone, 'is_album', is_album)
        )
        on conflict do nothing;
    end if;
  end if;

  begin
    if new.is_delete is false and is_shadowbanned = false then
      insert into notification
        (blocknumber, user_ids, timestamp, type, specifier, group_id, data)
        values
        (
          new.blocknumber,
          ARRAY [owner_user_id],
          new.created_at,
          'repost',
          new.user_id,
          'repost:' || new.repost_item_id || ':type:' || new.repost_type,
          json_build_object('repost_item_id', new.repost_item_id, 'user_id', new.user_id, 'type', new.repost_type)
        )
        on conflict do nothing;
    end if;

    if new.is_delete is false
       and new.is_repost_of_repost is true
       and is_shadowbanned = false then
      with
        followee_repost_of_repost_ids as (
          select user_id
          from reposts r
          where r.repost_item_id = new.repost_item_id
            and new.created_at - INTERVAL '1 month' < r.created_at
            and new.created_at > r.created_at
            and r.is_delete is false
            and r.is_current is true
            and r.user_id in (
              select followee_user_id
              from follows
              where follower_user_id = new.user_id
                and is_delete is false
                and is_current is true
            )
        )
      insert into notification
        (blocknumber, user_ids, timestamp, type, specifier, group_id, data)
      SELECT blocknumber_val, user_ids_val, timestamp_val, type_val, specifier_val, group_id_val, data_val
      FROM (
        SELECT new.blocknumber AS blocknumber_val,
        ARRAY(
          SELECT user_id FROM followee_repost_of_repost_ids
        ) AS user_ids_val,
        new.created_at AS timestamp_val,
        'repost_of_repost' AS type_val,
        new.user_id AS specifier_val,
        'repost_of_repost:' || new.repost_item_id || ':type:' || new.repost_type AS group_id_val,
        json_build_object(
          'repost_of_repost_item_id', new.repost_item_id,
          'user_id', new.user_id,
          'type', case when is_album then 'album' else new.repost_type::text end
        ) AS data_val
      ) sub
      WHERE user_ids_val IS NOT NULL AND array_length(user_ids_val, 1) > 0
      on conflict do nothing;
    end if;

    if new.is_delete is false and new.repost_type::text = 'track' and track_remix_of is not null and is_shadowbanned = false then
      select case when tracks.owner_id = new.user_id then TRUE else FALSE end into is_remix_cosign
        from tracks
        where is_current and track_id = (track_remix_of->'tracks'->0->>'parent_track_id')::int;
      if is_remix_cosign then
        insert into notification
          (blocknumber, user_ids, timestamp, type, specifier, group_id, data)
          values
          (
            new.blocknumber,
            ARRAY [owner_user_id],
            new.created_at,
            'cosign',
            new.user_id,
            'cosign:parent_track' || (track_remix_of->'tracks'->0->>'parent_track_id')::int || ':original_track:' || new.repost_item_id,
            json_build_object('parent_track_id', (track_remix_of->'tracks'->0->>'parent_track_id')::int, 'track_id', new.repost_item_id, 'track_owner_id', owner_user_id)
          )
        on conflict do nothing;
      end if;
    end if;
  exception
    when others then
      raise warning 'An error occurred in %: %', tg_name, sqlerrm;
  end;

  return null;

exception
  when others then
    raise warning 'An error occurred in %: %', tg_name, sqlerrm;
    return null;
end;
$$ LANGUAGE plpgsql;

DO $$ BEGIN
  CREATE TRIGGER on_repost
  AFTER INSERT ON reposts
  FOR EACH ROW EXECUTE PROCEDURE handle_repost();
EXCEPTION
  WHEN others THEN NULL;
END $$;
