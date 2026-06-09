-- track_collaborators stores collaborator invites on tracks.
--
-- The track owner tags collaborators in track Create/Update metadata
-- (`collaborators: [user_id, ...]`), which the track handlers reconcile into
-- 'pending' rows here. A collaborator then accepts/declines via a
-- TrackCollaborator Approve/Reject ManageEntity tx, flipping status to
-- 'accepted'/'rejected'. Only 'accepted' rows surface a track on the
-- collaborator's profile and merged dashboard.
--
-- Modeled on the manager-invite (grants) flow: pending -> accepted/rejected.

CREATE TABLE IF NOT EXISTS track_collaborators (
  track_id integer NOT NULL,
  collaborator_user_id integer NOT NULL,
  invited_by integer NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  created_at timestamp without time zone NOT NULL,
  updated_at timestamp without time zone NOT NULL,
  txhash character varying NOT NULL,
  blocknumber integer,
  CONSTRAINT track_collaborators_pkey PRIMARY KEY (track_id, collaborator_user_id),
  CONSTRAINT track_collaborators_status_check CHECK (status IN ('pending', 'accepted', 'rejected'))
);

-- Profile / dashboard merge (hot path): fetch a user's accepted collaborations
-- fast. Covering (track_id included) so the lookup is index-only.
CREATE INDEX IF NOT EXISTS idx_track_collaborators_collaborator
  ON track_collaborators (collaborator_user_id, status, track_id);

-- Track render: list a track's collaborators.
CREATE INDEX IF NOT EXISTS idx_track_collaborators_track
  ON track_collaborators (track_id);
