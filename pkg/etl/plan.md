---
name: ETL Entity Manager Parity
overview: Implement full entity manager processing parity with discovery-provider in the go-openaudio ETL package, using graphite-stacked PRs.
todos:
  - id: foundation
    content: "PR1: Foundation — handler framework, dispatcher, test infra, migrations, debug logging, local runner"
    status: completed
  - id: user-create
    content: "PR2: User Create handler"
    status: completed
  - id: user-update
    content: "PR3: User Update handler (markNotCurrent, metadata merge)"
    status: completed
  - id: user-verify
    content: "PR4: User Verify handler (0003 migration, verified_address)"
    status: completed
  - id: track-crud
    content: "PR5: Track Create/Update/Delete handlers (genre allowlist, slug collision, routes)"
    status: completed
  - id: playlist-social-apps-grants
    content: "PR6: Playlist Create/Update/Delete, Follow/Unfollow, Save/Unsave, Repost/Unrepost, DeveloperApp CRUD, Grant CRUD"
    status: completed
  - id: remaining-entities
    content: "Future: Comments, Notifications, AssociatedWallet, DashboardWalletUser, Tip, MutedUser, etc."
    status: pending
isProject: false
---

# ETL Entity Manager Parity Plan

## Current State

All core entity manager handlers are implemented with full validation parity against the discovery-provider celery indexer. The ETL indexer processes ManageEntity transactions through a dispatcher that routes to per-entity-action handlers, each performing stateless validation, stateful validation, and domain table writes.

## Graphite Stack

```
main
  └── rj/etl-em-user-update       PR #149 — User Update
       └── rj/etl-em-user-verify   PR #150 — User Verify
            └── rj/etl-em-track-create  PR #176 — Track Create/Update/Delete
                 └── rj/etl-em-playlist-create  PR #177 — Playlist + Social + DeveloperApp + Grant
```

## Implemented Handlers (20 total)

| Entity | Actions | Handler Files |
|--------|---------|---------------|
| User | Create, Update, Verify | `user_create.go`, `user_update.go`, `user_verify.go` |
| Track | Create, Update, Delete | `track_create.go`, `track_update.go`, `track_delete.go` |
| Playlist | Create, Update, Delete | `playlist_create.go`, `playlist_update.go`, `playlist_delete.go` |
| Follow | Follow, Unfollow | `social_follow.go` |
| Save | Save, Unsave | `social_save.go` |
| Repost | Repost, Unrepost | `social_repost.go` |
| DeveloperApp | Create, Update, Delete | `developer_app_create.go`, `developer_app_update.go`, `developer_app_delete.go` |
| Grant | Create, Delete, Approve, Reject | `grant_create.go`, `grant_revoke.go` |

## Remaining Entities (lower priority)

These are less critical for indexing parity and can be added incrementally:

- **Comments**: Create, Update, Delete, React, Unreact, Pin, Unpin, Report, Mute, Unmute
- **Notifications**: Create, View, ViewPlaylist
- **AssociatedWallet**: Create, Delete
- **DashboardWalletUser**: Create, Delete
- **Tip**: Update
- **MutedUser**: Mute, Unmute
- **EncryptedEmail, EmailAccess, Event**: Various actions

## What to Defer

- **Side effects** (challenge bus, notifications, trending updates) — not needed for indexing parity
- **Content access checks** (remixability, gated content) — add after core entities work
- **Non-entity-manager celery tasks** (per user's instruction)
