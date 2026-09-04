# Changelog

## [1.7.1](https://github.com/OpenAudio/go-openaudio/compare/pkg/etl/v1.7.0...pkg/etl/v1.7.1) (2026-09-04)


### Bug Fixes

* **etl:** carry email_access.is_initial so migrated grants stay decryptable ([#540](https://github.com/OpenAudio/go-openaudio/issues/540)) ([229cc04](https://github.com/OpenAudio/go-openaudio/commit/229cc04ff7c909808302a51cfccbb7b45a876590))
* **etl:** don't let a deleted artist pick block every profile edit ([#510](https://github.com/OpenAudio/go-openaudio/issues/510)) ([81f4af5](https://github.com/OpenAudio/go-openaudio/commit/81f4af523801558b9a8673c010829f8f9a14304a))
* **etl:** exit bounded runs instead of hanging in Wait() ([#512](https://github.com/OpenAudio/go-openaudio/issues/512)) ([570b44f](https://github.com/OpenAudio/go-openaudio/commit/570b44fefe23e609933a28ef451f723d055a8fcd))
* **etl:** persist DDEX rights metadata on track create ([#517](https://github.com/OpenAudio/go-openaudio/issues/517)) ([88b83b3](https://github.com/OpenAudio/go-openaudio/commit/88b83b350bfa51d32a62b756713b8d36084c0c9a))
* **etl:** persist track_downloads.created_at instead of defaulting to now() ([#515](https://github.com/OpenAudio/go-openaudio/issues/515)) ([c16e46a](https://github.com/OpenAudio/go-openaudio/commit/c16e46aacf45e7406880b4bef2c099cd49e0fa46))
* **etl:** project access_authorities from the field core enforces ([#541](https://github.com/OpenAudio/go-openaudio/issues/541)) ([5e6bd39](https://github.com/OpenAudio/go-openaudio/commit/5e6bd390939a893b008cc2887121ee5414d96d75))
* **etl:** resume from MAX(block_height) and honor --start ([#513](https://github.com/OpenAudio/go-openaudio/issues/513)) ([6a2724b](https://github.com/OpenAudio/go-openaudio/commit/6a2724b653184509ed6bf93c7bfbf19f6904db53))
* **genesis-writer:** replay comments as edited when the source says they were ([#539](https://github.com/OpenAudio/go-openaudio/issues/539)) ([4baa62a](https://github.com/OpenAudio/go-openaudio/commit/4baa62a28fa1d21ce2feb3a265b3d4e5e904c153))
* **parity:** compare playlists_containing_track as a set, accept duration NULL-&gt;0 ([#518](https://github.com/OpenAudio/go-openaudio/issues/518)) ([ad8a40a](https://github.com/OpenAudio/go-openaudio/commit/ad8a40a0807759eff7903a88cf5c7d5e51cc1c2a))
* **parity:** stop keying track_downloads on txhash ([#516](https://github.com/OpenAudio/go-openaudio/issues/516)) ([6e7253e](https://github.com/OpenAudio/go-openaudio/commit/6e7253ebd368511c05ae7642129810c75c9b918b))

## [1.7.0](https://github.com/OpenAudio/go-openaudio/compare/pkg/etl/v1.6.4...pkg/etl/v1.7.0) (2026-08-10)


### Features

* **etl:** carry playlist removal history through the migration ([#485](https://github.com/OpenAudio/go-openaudio/issues/485)) ([222a6b4](https://github.com/OpenAudio/go-openaudio/commit/222a6b4bc575dc06093b9ecc9de31ae9038d5228))
* **etl:** make entity_id the canonical subscription target for both types ([#470](https://github.com/OpenAudio/go-openaudio/issues/470)) ([b137701](https://github.com/OpenAudio/go-openaudio/commit/b13770197842d46c10af385b89c4d16b477fddfc))
* **etl:** store the payout wallet, coin flair and profile type users send ([#453](https://github.com/OpenAudio/go-openaudio/issues/453)) ([7753833](https://github.com/OpenAudio/go-openaudio/commit/7753833bf1ae782ebfa6a99e90f4ab4c39a3d16c))
* **genesis-writer:** carry profile settings and grant approval on migrated creates ([#492](https://github.com/OpenAudio/go-openaudio/issues/492)) ([06d9e62](https://github.com/OpenAudio/go-openaudio/commit/06d9e624ae25dd922329d0d57faf397322955b52))
* **parity:** compare column contents, not just row counts ([#484](https://github.com/OpenAudio/go-openaudio/issues/484)) ([bf016d6](https://github.com/OpenAudio/go-openaudio/commit/bf016d64241da26b7b1be18c369e4e51523c58c3))


### Bug Fixes

* **etl:** do not imply a subscription for migrated follows ([#473](https://github.com/OpenAudio/go-openaudio/issues/473)) ([5293b49](https://github.com/OpenAudio/go-openaudio/commit/5293b497e54eabe92c020f7b772f09576dbd6276))
* **etl:** keep the slug a migrated playlist already serves ([#479](https://github.com/OpenAudio/go-openaudio/issues/479)) ([b0bd305](https://github.com/OpenAudio/go-openaudio/commit/b0bd305308f1e59b541168dbc81c9dd96659ee8c))
* **etl:** key subscription identity on entity_type, not just numeric id ([#469](https://github.com/OpenAudio/go-openaudio/issues/469)) ([7d7f7d6](https://github.com/OpenAudio/go-openaudio/commit/7d7f7d6a6f5badbe3d011a9bbf5e7cc8e9862b0c))
* **etl:** maintain the playlist reverse index on tracks ([#481](https://github.com/OpenAudio/go-openaudio/issues/481)) ([95f8e2f](https://github.com/OpenAudio/go-openaudio/commit/95f8e2ff0c66573920488e272dc53ca245c57343))
* **etl:** persist every user field the create contract accepts ([#466](https://github.com/OpenAudio/go-openaudio/issues/466)) ([db5c632](https://github.com/OpenAudio/go-openaudio/commit/db5c632c3a94fe3e6289360ee25877491ea14b79))
* **etl:** persist profile_type on user Create ([#458](https://github.com/OpenAudio/go-openaudio/issues/458)) ([a60f996](https://github.com/OpenAudio/go-openaudio/commit/a60f996ec83b2bade5aa334f054161d5ec9ddd2c))
* **etl:** record migrated entities with their original created_at ([#439](https://github.com/OpenAudio/go-openaudio/issues/439)) ([a0aa0e8](https://github.com/OpenAudio/go-openaudio/commit/a0aa0e81975f4ef3074171b4305469f4a34ce7b1))
* **etl:** replay concluded remix contests through the migration ([#494](https://github.com/OpenAudio/go-openaudio/issues/494)) ([28a05c3](https://github.com/OpenAudio/go-openaudio/commit/28a05c3d86dbbfa532b1c15a882deffa5cefb380))
* **etl:** report transactions that match no handler ([#480](https://github.com/OpenAudio/go-openaudio/issues/480)) ([36c806c](https://github.com/OpenAudio/go-openaudio/commit/36c806ca96dedff61b647d6b3ff72d08ecdeef9e))
* **etl:** store the DDEX rights metadata tracks already send ([#461](https://github.com/OpenAudio/go-openaudio/issues/461)) ([c1c8993](https://github.com/OpenAudio/go-openaudio/commit/c1c899310c323c162fa152a9c0243954fd61b86e))
* **etl:** write playlist_tracks timestamps in block time ([#482](https://github.com/OpenAudio/go-openaudio/issues/482)) ([733c8b2](https://github.com/OpenAudio/go-openaudio/commit/733c8b26e4094682a9bffe293d625c4262092fb5))
* **genesis-writer:** emit six missing track state flags ([#486](https://github.com/OpenAudio/go-openaudio/issues/486)) ([3a4883a](https://github.com/OpenAudio/go-openaudio/commit/3a4883a6259d4599f548f6a6d90d362e862ff13e))
* **genesis:** carry soft-deleted state on Create instead of dropping the row ([#455](https://github.com/OpenAudio/go-openaudio/issues/455)) ([74e3a70](https://github.com/OpenAudio/go-openaudio/commit/74e3a70d160da6775648b1aef1604a29bf6d73c3))
* **genesis:** carry the entity fields the writer never put on Create ([#445](https://github.com/OpenAudio/go-openaudio/issues/445)) ([947c053](https://github.com/OpenAudio/go-openaudio/commit/947c053f1a181d7ee018d0f589ba61654d221f6a))
* **genesis:** keep each track's existing slug instead of regenerating it ([#452](https://github.com/OpenAudio/go-openaudio/issues/452)) ([49841ce](https://github.com/OpenAudio/go-openaudio/commit/49841cea93aa5f796820e32f52b64c4ce4f8d883))
* **genesis:** migrate soft-deleted wallet links by carrying is_delete on Create ([#442](https://github.com/OpenAudio/go-openaudio/issues/442)) ([cb5abe8](https://github.com/OpenAudio/go-openaudio/commit/cb5abe865c068758117177304fad794bb34c9b03))
* **parity:** do not skip every row when the reference is a snapshot ([#475](https://github.com/OpenAudio/go-openaudio/issues/475)) ([72da008](https://github.com/OpenAudio/go-openaudio/commit/72da0082a267b98a6b01248cf59746252b024b1a))


### Performance Improvements

* **etl:** drop the per-transaction savepoint when replaying migration blocks ([#451](https://github.com/OpenAudio/go-openaudio/issues/451)) ([4639757](https://github.com/OpenAudio/go-openaudio/commit/46397573275d9208e58c83557534c2d47a996698))

## [1.6.4](https://github.com/OpenAudio/go-openaudio/compare/pkg/etl/v1.6.3...pkg/etl/v1.6.4) (2026-08-05)


### Bug Fixes

* **ci:** stop stamping the etl version onto a dependency requirement ([#431](https://github.com/OpenAudio/go-openaudio/issues/431)) ([ec8df1f](https://github.com/OpenAudio/go-openaudio/commit/ec8df1fb3702c522bf39a45701d50bc5c5b25b1f))
* **etl:** don't delete rows from an ETL migration ([#433](https://github.com/OpenAudio/go-openaudio/issues/433)) ([30b1c1b](https://github.com/OpenAudio/go-openaudio/commit/30b1c1b9d24563858b0ce8886056f0415f9bd056))

## [1.6.3](https://github.com/OpenAudio/go-openaudio/compare/pkg/etl/v1.6.2...pkg/etl/v1.6.3) (2026-08-04)


### Bug Fixes

* **etl:** index genesis-migration entities without data loss ([#425](https://github.com/OpenAudio/go-openaudio/issues/425)) ([6c116fa](https://github.com/OpenAudio/go-openaudio/commit/6c116fa1841eb8836e43ef162a40b757b7b1ecd3))
* **etl:** persist orig_filename and stop updates from unlinking stems ([#421](https://github.com/OpenAudio/go-openaudio/issues/421)) ([1d9f697](https://github.com/OpenAudio/go-openaudio/commit/1d9f69772e870ec920a2dcef3c12328947dbd57d))
* **etl:** record album saves and reposts as playlist ([#428](https://github.com/OpenAudio/go-openaudio/issues/428)) ([26ccde7](https://github.com/OpenAudio/go-openaudio/commit/26ccde79181ff8c3dd1e711070adbb7327e6be68))

## [1.6.2](https://github.com/OpenAudio/go-openaudio/compare/pkg/etl/v1.6.1...pkg/etl/v1.6.2) (2026-07-16)


### Bug Fixes

* **etl:** don't let track updates wipe CIDs via explicit null ([#410](https://github.com/OpenAudio/go-openaudio/issues/410)) ([b4a5ebe](https://github.com/OpenAudio/go-openaudio/commit/b4a5ebe0698ff11ff3fdaf57de3f3aa881d704e7))

## [1.6.1](https://github.com/OpenAudio/go-openaudio/compare/pkg/etl/v1.6.0...pkg/etl/v1.6.1) (2026-07-13)


### Bug Fixes

* **etl:** restore migration 0034 stub ([#401](https://github.com/OpenAudio/go-openaudio/issues/401)) ([50e54b5](https://github.com/OpenAudio/go-openaudio/commit/50e54b5c236ed71437c3b379f6f5fd05b8e0af36))

## [1.6.0](https://github.com/OpenAudio/go-openaudio/compare/pkg/etl/v1.5.0...pkg/etl/v1.6.0) (2026-07-01)


### Features

* **etl:** support ManageEntityMigration transactions ([#375](https://github.com/OpenAudio/go-openaudio/issues/375)) ([dfabacb](https://github.com/OpenAudio/go-openaudio/commit/dfabacb346d9ff3945421b06a21db7b11cda76c1))


### Bug Fixes

* persist user deactivation updates ([#394](https://github.com/OpenAudio/go-openaudio/issues/394)) ([b75c8bb](https://github.com/OpenAudio/go-openaudio/commit/b75c8bb6f8c5c6e58175894d84cc72a2462aaae8))


### Reverts

* remove event_routes table and slug handling ([#386](https://github.com/OpenAudio/go-openaudio/issues/386)) ([ca62eef](https://github.com/OpenAudio/go-openaudio/commit/ca62eefc1bb44fce0e311ea0f74030ea03b3236b))

## [1.5.0](https://github.com/OpenAudio/go-openaudio/compare/pkg/etl/v1.4.0...pkg/etl/v1.5.0) (2026-06-18)


### Features

* **etl:** normalize genre on entity_manager track writes ([#367](https://github.com/OpenAudio/go-openaudio/issues/367)) ([5aa118b](https://github.com/OpenAudio/go-openaudio/commit/5aa118b67bc19474a56f81a6665f2f47854d72e4))

## [1.4.0](https://github.com/OpenAudio/go-openaudio/compare/pkg/etl/v1.3.0...pkg/etl/v1.4.0) (2026-06-15)


### Features

* **etl:** add event_routes for contest permalink support ([#354](https://github.com/OpenAudio/go-openaudio/issues/354)) ([97ccec3](https://github.com/OpenAudio/go-openaudio/commit/97ccec32c1c62f6bd0f8c0aee68191cafbad6523))
* **etl:** add TrackCollaborator to the entity manager spec ([#345](https://github.com/OpenAudio/go-openaudio/issues/345)) ([550356d](https://github.com/OpenAudio/go-openaudio/commit/550356d6fdf88301678fccf59ee0e2a2562c0488))
* **etl:** auto-subscribe uploader to remix-contest event on Track Create ([#311](https://github.com/OpenAudio/go-openaudio/issues/311)) ([1b70465](https://github.com/OpenAudio/go-openaudio/commit/1b70465de8b7d45ccec45e9d38d04e34a9a5e2f6))
* **etl:** comments.is_members_only + video_url for fan club text posts ([#312](https://github.com/OpenAudio/go-openaudio/issues/312)) ([3b99954](https://github.com/OpenAudio/go-openaudio/commit/3b99954f85383b3066a10c95b7312ad7653bcae6))
* **etl:** consume blocks via gRPC StreamBlocks with catch-up + fallback ([#342](https://github.com/OpenAudio/go-openaudio/issues/342)) ([aee578e](https://github.com/OpenAudio/go-openaudio/commit/aee578ec923b789a8c97b361642659a458473313))
* **etl:** let a collaborator leave a track after accepting ([#349](https://github.com/OpenAudio/go-openaudio/issues/349)) ([22066c3](https://github.com/OpenAudio/go-openaudio/commit/22066c3521d3f6d60578e7f879899689bbc6b58d))
* **etl:** post-handler hooks for User/Track/Playlist Create + generic registry ([#317](https://github.com/OpenAudio/go-openaudio/issues/317)) ([1c75995](https://github.com/OpenAudio/go-openaudio/commit/1c7599596a27e376d7e374a7543b8a5abe826a76))
* **etl:** post-write hook for Plays transactions ([#322](https://github.com/OpenAudio/go-openaudio/issues/322)) ([4d1c9df](https://github.com/OpenAudio/go-openaudio/commit/4d1c9dfdfb529bad111e03a3d10221c25b48677f))


### Bug Fixes

* **etl:** canonicalize playlist_contents entry keys on write ([#321](https://github.com/OpenAudio/go-openaudio/issues/321)) ([35ad422](https://github.com/OpenAudio/go-openaudio/commit/35ad422c0f2753c33f9b709dc91bcab626aac9a9))
* **etl:** count runes not bytes in name/bio/handle/description limits ([#340](https://github.com/OpenAudio/go-openaudio/issues/340)) ([7062cd9](https://github.com/OpenAudio/go-openaudio/commit/7062cd90dff56af681d738cad9c5cee2958e0445))
* **etl:** default release_date to created_at when unset (python parity) ([#333](https://github.com/OpenAudio/go-openaudio/issues/333)) ([959f1c7](https://github.com/OpenAudio/go-openaudio/commit/959f1c7db8e05178177afccd2f8bd2ada2ce3080))
* **etl:** drop incompatible developer_apps UNIQUE(address) + auto-seed block in tests ([#302](https://github.com/OpenAudio/go-openaudio/issues/302)) ([6885f9a](https://github.com/OpenAudio/go-openaudio/commit/6885f9a011f3df661086ad20ac330fb5f46d1a7b))
* **etl:** four prod-clone-run bugs + [#307](https://github.com/OpenAudio/go-openaudio/issues/307) test fixes folded in ([#308](https://github.com/OpenAudio/go-openaudio/issues/308)) ([4e0c89b](https://github.com/OpenAudio/go-openaudio/commit/4e0c89bce96b3043e7d6c99306438dafd8a7bb42))
* **etl:** halt on block indexing failure ([#323](https://github.com/OpenAudio/go-openaudio/issues/323)) ([819100b](https://github.com/OpenAudio/go-openaudio/commit/819100b28c94215b609c02f97c7338738bc1d4f1))
* **etl:** let a grant's grantee revoke their own user-to-user grant ([#360](https://github.com/OpenAudio/go-openaudio/issues/360)) ([5edfdf3](https://github.com/OpenAudio/go-openaudio/commit/5edfdf36a895d1a8a33e2416fffd3d30cac498e5))
* **etl:** persist dev-app image_url; rune-count video_url/redirect_uri ([#350](https://github.com/OpenAudio/go-openaudio/issues/350)) ([bda0b8b](https://github.com/OpenAudio/go-openaudio/commit/bda0b8b592f56289e4c397bdb063456a7e41743f))
* **etl:** persist dropped track + comment fields on write ([#343](https://github.com/OpenAudio/go-openaudio/issues/343)) ([c5fcacf](https://github.com/OpenAudio/go-openaudio/commit/c5fcacffbb7973b5dfe38912d529c6db4f591aca))
* **etl:** persist social links on user create/update ([#341](https://github.com/OpenAudio/go-openaudio/issues/341)) ([4cccb46](https://github.com/OpenAudio/go-openaudio/commit/4cccb46f1332d59e0913533c5fd303b2ca9073ec))
* **etl:** persist track bpm/musical_key/audio_upload_id (python parity) ([#334](https://github.com/OpenAudio/go-openaudio/issues/334)) ([dca79f0](https://github.com/OpenAudio/go-openaudio/commit/dca79f0306394816a56955677dd839bdb9fad3e2))
* **etl:** playlist_seen PK, ErrNoRows on soft-deleted rows, attribute dispatch errors ([#310](https://github.com/OpenAudio/go-openaudio/issues/310)) ([77cfaee](https://github.com/OpenAudio/go-openaudio/commit/77cfaee95650c3fed5b6edfb1e1a9a15a697e784))
* **etl:** populate playlists.last_added_to on track add ([#348](https://github.com/OpenAudio/go-openaudio/issues/348)) ([e8586ac](https://github.com/OpenAudio/go-openaudio/commit/e8586ac9ce16a9cd9dc7aa414c6c901186261513))
* **etl:** scan custom Postgres enums + dedup repeat tx inserts ([#305](https://github.com/OpenAudio/go-openaudio/issues/305)) ([f832a89](https://github.com/OpenAudio/go-openaudio/commit/f832a89abd1732bde62155d496d9bffb7ebea74d))
* **etl:** tolerate co-existing writers + process each block atomically ([#319](https://github.com/OpenAudio/go-openaudio/issues/319)) ([6ea0f10](https://github.com/OpenAudio/go-openaudio/commit/6ea0f10200acb802e6194b25a2b58c4b6b12131e))
* **etl:** tolerate timezone-less release_date formats ([#332](https://github.com/OpenAudio/go-openaudio/issues/332)) ([5ed068b](https://github.com/OpenAudio/go-openaudio/commit/5ed068b34326998e3c9ce8757ba523d106ebd817))
* **etl:** upsert explicit subscription writes (root cause of dupes) ([#335](https://github.com/OpenAudio/go-openaudio/issues/335)) ([5d2e19a](https://github.com/OpenAudio/go-openaudio/commit/5d2e19ad9d7849bd2c426de062e3079e2c2a1270))


### Performance Improvements

* **etl:** index route (owner_id, title_slug) for slug-collision lookups ([#337](https://github.com/OpenAudio/go-openaudio/issues/337)) ([43606e8](https://github.com/OpenAudio/go-openaudio/commit/43606e8d8e5aac580b9665c4deefdcfac0852cae))
* **etl:** stop maintaining blocks is_current ([#330](https://github.com/OpenAudio/go-openaudio/issues/330)) ([ea6c925](https://github.com/OpenAudio/go-openaudio/commit/ea6c925d2455b7615c3070ed455d743686de21a0))
* **etl:** upsert social writes in place instead of demote-then-insert ([#331](https://github.com/OpenAudio/go-openaudio/issues/331)) ([24084d2](https://github.com/OpenAudio/go-openaudio/commit/24084d2bf30baaef46baed6728d5c65df4709850))


### Code Refactoring

* **etl:** tighten track_collaborators reconcile ([#346](https://github.com/OpenAudio/go-openaudio/issues/346)) ([3904b9d](https://github.com/OpenAudio/go-openaudio/commit/3904b9d7046c9fa453d201736907709bc7469a13))
