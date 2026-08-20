# Changelog

## [1.11.0](https://github.com/OpenAudio/go-openaudio/compare/v1.10.0...v1.11.0) (2026-08-20)


### Features

* **storage:** precompute waveform peaks on the validator node ([#522](https://github.com/OpenAudio/go-openaudio/issues/522)) ([1cbc367](https://github.com/OpenAudio/go-openaudio/commit/1cbc3670d0f9504f85499ddb0531482caad9c81e))


### Bug Fixes

* **core:** honor OPENAUDIO_GRPC_LADDR instead of hardcoding :50051 ([#533](https://github.com/OpenAudio/go-openaudio/issues/533)) ([d5cef7c](https://github.com/OpenAudio/go-openaudio/commit/d5cef7c59a0b17841b802756861699a4611ea46b))
* **etl:** carry email_access.is_initial so migrated grants stay decryptable ([#540](https://github.com/OpenAudio/go-openaudio/issues/540)) ([229cc04](https://github.com/OpenAudio/go-openaudio/commit/229cc04ff7c909808302a51cfccbb7b45a876590))
* **etl:** don't let a deleted artist pick block every profile edit ([#510](https://github.com/OpenAudio/go-openaudio/issues/510)) ([81f4af5](https://github.com/OpenAudio/go-openaudio/commit/81f4af523801558b9a8673c010829f8f9a14304a))
* **etl:** persist DDEX rights metadata on track create ([#517](https://github.com/OpenAudio/go-openaudio/issues/517)) ([88b83b3](https://github.com/OpenAudio/go-openaudio/commit/88b83b350bfa51d32a62b756713b8d36084c0c9a))
* **etl:** persist track_downloads.created_at instead of defaulting to now() ([#515](https://github.com/OpenAudio/go-openaudio/issues/515)) ([c16e46a](https://github.com/OpenAudio/go-openaudio/commit/c16e46aacf45e7406880b4bef2c099cd49e0fa46))
* **etl:** project access_authorities from the field core enforces ([#541](https://github.com/OpenAudio/go-openaudio/issues/541)) ([5e6bd39](https://github.com/OpenAudio/go-openaudio/commit/5e6bd390939a893b008cc2887121ee5414d96d75))
* **genesis-writer:** emit allowed_api_keys so migrated tracks keep it ([#542](https://github.com/OpenAudio/go-openaudio/issues/542)) ([d3aed53](https://github.com/OpenAudio/go-openaudio/commit/d3aed53b0d27554507c3c9ff545aa2f06160767e))
* **genesis-writer:** emit one grant per (user, grantee), newest wins ([#532](https://github.com/OpenAudio/go-openaudio/issues/532)) ([d288865](https://github.com/OpenAudio/go-openaudio/commit/d28886574460ebcaa6d9fafd7d4196ff7e5c6ed7))
* **genesis-writer:** emit release_date as RFC3339, not Postgres text ([#519](https://github.com/OpenAudio/go-openaudio/issues/519)) ([5a8d4a4](https://github.com/OpenAudio/go-openaudio/commit/5a8d4a4ed759bbc6ec1edb12fd9060629c01cfb7))
* **genesis-writer:** fail the step that loses transactions before flush ([#535](https://github.com/OpenAudio/go-openaudio/issues/535)) ([1187b3c](https://github.com/OpenAudio/go-openaudio/commit/1187b3cecdd1a776dd274244248fac2712c8f802))
* **genesis-writer:** flush and drain before checkpointing a step ([#521](https://github.com/OpenAudio/go-openaudio/issues/521)) ([d8e5321](https://github.com/OpenAudio/go-openaudio/commit/d8e5321af61975378ea109ab1aeba77aa26dbb8c))
* **genesis-writer:** keep sub-second precision on every emitted timestamp ([#534](https://github.com/OpenAudio/go-openaudio/issues/534)) ([bddde9e](https://github.com/OpenAudio/go-openaudio/commit/bddde9e0ee8dde7990e667f402b40082f023b07c))
* **genesis-writer:** replay comments as edited when the source says they were ([#539](https://github.com/OpenAudio/go-openaudio/issues/539)) ([4baa62a](https://github.com/OpenAudio/go-openaudio/commit/4baa62a28fa1d21ce2feb3a265b3d4e5e904c153))
* **genesis-writer:** run events before comments ([#520](https://github.com/OpenAudio/go-openaudio/issues/520)) ([43679ef](https://github.com/OpenAudio/go-openaudio/commit/43679ef45d3bb575dca1c40922115319d6c6c1b9))
* **mediorum:** prune storage to replication factor ([#537](https://github.com/OpenAudio/go-openaudio/issues/537)) ([865fb8d](https://github.com/OpenAudio/go-openaudio/commit/865fb8d86ea84ca7c730ad936efc1c3c6ecd6a7a))
* **parity:** compare playlists_containing_track as a set, accept duration NULL-&gt;0 ([#518](https://github.com/OpenAudio/go-openaudio/issues/518)) ([ad8a40a](https://github.com/OpenAudio/go-openaudio/commit/ad8a40a0807759eff7903a88cf5c7d5e51cc1c2a))
* **storage:** don't challenge cids that cannot be proven yet ([#544](https://github.com/OpenAudio/go-openaudio/issues/544)) ([7a2679a](https://github.com/OpenAudio/go-openaudio/commit/7a2679a9a24be707938ce38cfddc1f8691b15199))
* **storage:** report isDbLocalhost from the DSN host ([#525](https://github.com/OpenAudio/go-openaudio/issues/525)) ([906e5f6](https://github.com/OpenAudio/go-openaudio/commit/906e5f61533a43beb25f32e6a07e4a510629f409))


### Code Refactoring

* **storage:** remove the unused tus replication path ([#529](https://github.com/OpenAudio/go-openaudio/issues/529)) ([67aee5e](https://github.com/OpenAudio/go-openaudio/commit/67aee5e2f90535ef89f0605251899ad43eedb3fc))

## [1.10.0](https://github.com/OpenAudio/go-openaudio/compare/v1.9.0...v1.10.0) (2026-08-13)


### Features

* **genesis-writer:** read rewards from the old chain's postgres ([#496](https://github.com/OpenAudio/go-openaudio/issues/496)) ([0059c0d](https://github.com/OpenAudio/go-openaudio/commit/0059c0db4cbb772583cc060ecc6f0089d8dad42c))
* **genesis-writer:** rebuild rewards from table state, not replayed transactions ([#504](https://github.com/OpenAudio/go-openaudio/issues/504)) ([db54989](https://github.com/OpenAudio/go-openaudio/commit/db5498932f67c0e8302b63dabe725e6b91a5975d))
* **mediorum:** pull replicated blobs from storage ([#497](https://github.com/OpenAudio/go-openaudio/issues/497)) ([6daf1db](https://github.com/OpenAudio/go-openaudio/commit/6daf1dbc4c56fc7066d15c16b97dbfc2767938ee))


### Bug Fixes

* **etl:** exit bounded runs instead of hanging in Wait() ([#512](https://github.com/OpenAudio/go-openaudio/issues/512)) ([570b44f](https://github.com/OpenAudio/go-openaudio/commit/570b44fefe23e609933a28ef451f723d055a8fcd))
* **etl:** resume from MAX(block_height) and honor --start ([#513](https://github.com/OpenAudio/go-openaudio/issues/513)) ([6a2724b](https://github.com/OpenAudio/go-openaudio/commit/6a2724b653184509ed6bf93c7bfbf19f6904db53))
* **genesis-writer:** resume from the seen commit, not the block commit ([#505](https://github.com/OpenAudio/go-openaudio/issues/505)) ([ddd0a71](https://github.com/OpenAudio/go-openaudio/commit/ddd0a717380e8c07f494bc7c0995e7f6d42f9208))
* **genesis-writer:** stop disabling autovacuum during replay ([#511](https://github.com/OpenAudio/go-openaudio/issues/511)) ([0aaa0dc](https://github.com/OpenAudio/go-openaudio/commit/0aaa0dc5a44f9432fe96f8c93bde6db3b854c606))
* **openaudio:** point the embedded ETL at the configured HTTP port ([#507](https://github.com/OpenAudio/go-openaudio/issues/507)) ([ab10fa5](https://github.com/OpenAudio/go-openaudio/commit/ab10fa53f7be9162e0c1ce994c92237e06a1d4c9))


### Code Refactoring

* **genesis-writer:** run rewards first so an unreachable source fails fast ([#506](https://github.com/OpenAudio/go-openaudio/issues/506)) ([faa6fb7](https://github.com/OpenAudio/go-openaudio/commit/faa6fb7465e2444eb473efceeb07427c2d5620f5))

## [1.9.0](https://github.com/OpenAudio/go-openaudio/compare/v1.8.2...v1.9.0) (2026-08-10)


### Features

* **core:** add height-gated consensus ruleset engine ([#447](https://github.com/OpenAudio/go-openaudio/issues/447)) ([708f6d6](https://github.com/OpenAudio/go-openaudio/commit/708f6d68a9d0d1282dd7e9a7ee06331cf61e7bd5))
* **core:** authorize track cids against validator upload attestations ([#477](https://github.com/OpenAudio/go-openaudio/issues/477)) ([878a6a7](https://github.com/OpenAudio/go-openaudio/commit/878a6a706680cd7a7e55269bd9a0a6b750c715e7))
* **core:** enforce manage entity authorization behind a height gate ([#450](https://github.com/OpenAudio/go-openaudio/issues/450)) ([97140a6](https://github.com/OpenAudio/go-openaudio/commit/97140a69e32fced9edf930b500a57e61c92be285))
* **core:** track authorization state at consensus ([#448](https://github.com/OpenAudio/go-openaudio/issues/448)) ([3b5730c](https://github.com/OpenAudio/go-openaudio/commit/3b5730c3f2af6a82701150f8a6935216467c13b6))
* **etl:** carry playlist removal history through the migration ([#485](https://github.com/OpenAudio/go-openaudio/issues/485)) ([222a6b4](https://github.com/OpenAudio/go-openaudio/commit/222a6b4bc575dc06093b9ecc9de31ae9038d5228))
* **etl:** make entity_id the canonical subscription target for both types ([#470](https://github.com/OpenAudio/go-openaudio/issues/470)) ([b137701](https://github.com/OpenAudio/go-openaudio/commit/b13770197842d46c10af385b89c4d16b477fddfc))
* **etl:** store the payout wallet, coin flair and profile type users send ([#453](https://github.com/OpenAudio/go-openaudio/issues/453)) ([7753833](https://github.com/OpenAudio/go-openaudio/commit/7753833bf1ae782ebfa6a99e90f4ab4c39a3d16c))
* **genesis-writer:** carry profile settings and grant approval on migrated creates ([#492](https://github.com/OpenAudio/go-openaudio/issues/492)) ([06d9e62](https://github.com/OpenAudio/go-openaudio/commit/06d9e624ae25dd922329d0d57faf397322955b52))
* **genesis-writer:** migrate pinned comments ([#487](https://github.com/OpenAudio/go-openaudio/issues/487)) ([0cbaa69](https://github.com/OpenAudio/go-openaudio/commit/0cbaa697b9e532fdeddf8c563577144944d4893d))
* **mediorum:** attribute uploads to an asserted user and attest their cids ([#476](https://github.com/OpenAudio/go-openaudio/issues/476)) ([123f53e](https://github.com/OpenAudio/go-openaudio/commit/123f53ea40fa1176268db264f44425d2c7d7c41a))
* **mediorum:** operator-run prune job, and infer data loss from repair failures ([#437](https://github.com/OpenAudio/go-openaudio/issues/437)) ([e62185a](https://github.com/OpenAudio/go-openaudio/commit/e62185ac01037891b3647c97ccc3a138e184c498))
* **parity:** compare column contents, not just row counts ([#484](https://github.com/OpenAudio/go-openaudio/issues/484)) ([bf016d6](https://github.com/OpenAudio/go-openaudio/commit/bf016d64241da26b7b1be18c369e4e51523c58c3))


### Bug Fixes

* **ci:** stop stamping the etl version onto a dependency requirement ([#431](https://github.com/OpenAudio/go-openaudio/issues/431)) ([ec8df1f](https://github.com/OpenAudio/go-openaudio/commit/ec8df1fb3702c522bf39a45701d50bc5c5b25b1f))
* **core:** keep jailed validators valid for mediorum operations ([#468](https://github.com/OpenAudio/go-openaudio/issues/468)) ([f0b14d7](https://github.com/OpenAudio/go-openaudio/commit/f0b14d7b22f01df2b2e95a6075f2ccb9b6b87bbc))
* **core:** keep migration transactions out of the custom mempool ([#457](https://github.com/OpenAudio/go-openaudio/issues/457)) ([4a97d21](https://github.com/OpenAudio/go-openaudio/commit/4a97d21a7ffe95d0fd322df12f348afc750197e3))
* **core:** serve a block's transactions in block order ([#443](https://github.com/OpenAudio/go-openaudio/issues/443)) ([a6f3e5b](https://github.com/OpenAudio/go-openaudio/commit/a6f3e5b0ef20911bf7b85f0014a652aa6be36491))
* **dev:** prune blockstore on devnet nodes 3 and 4 ([#426](https://github.com/OpenAudio/go-openaudio/issues/426)) ([fc6c83f](https://github.com/OpenAudio/go-openaudio/commit/fc6c83f76597a30ab7374d6f2fdacb911c904e25))
* **etl:** do not imply a subscription for migrated follows ([#473](https://github.com/OpenAudio/go-openaudio/issues/473)) ([5293b49](https://github.com/OpenAudio/go-openaudio/commit/5293b497e54eabe92c020f7b772f09576dbd6276))
* **etl:** don't delete rows from an ETL migration ([#433](https://github.com/OpenAudio/go-openaudio/issues/433)) ([30b1c1b](https://github.com/OpenAudio/go-openaudio/commit/30b1c1b9d24563858b0ce8886056f0415f9bd056))
* **etl:** index genesis-migration entities without data loss ([#425](https://github.com/OpenAudio/go-openaudio/issues/425)) ([6c116fa](https://github.com/OpenAudio/go-openaudio/commit/6c116fa1841eb8836e43ef162a40b757b7b1ecd3))
* **etl:** keep the slug a migrated playlist already serves ([#479](https://github.com/OpenAudio/go-openaudio/issues/479)) ([b0bd305](https://github.com/OpenAudio/go-openaudio/commit/b0bd305308f1e59b541168dbc81c9dd96659ee8c))
* **etl:** key subscription identity on entity_type, not just numeric id ([#469](https://github.com/OpenAudio/go-openaudio/issues/469)) ([7d7f7d6](https://github.com/OpenAudio/go-openaudio/commit/7d7f7d6a6f5badbe3d011a9bbf5e7cc8e9862b0c))
* **etl:** maintain the playlist reverse index on tracks ([#481](https://github.com/OpenAudio/go-openaudio/issues/481)) ([95f8e2f](https://github.com/OpenAudio/go-openaudio/commit/95f8e2ff0c66573920488e272dc53ca245c57343))
* **etl:** persist every user field the create contract accepts ([#466](https://github.com/OpenAudio/go-openaudio/issues/466)) ([db5c632](https://github.com/OpenAudio/go-openaudio/commit/db5c632c3a94fe3e6289360ee25877491ea14b79))
* **etl:** persist profile_type on user Create ([#458](https://github.com/OpenAudio/go-openaudio/issues/458)) ([a60f996](https://github.com/OpenAudio/go-openaudio/commit/a60f996ec83b2bade5aa334f054161d5ec9ddd2c))
* **etl:** record album saves and reposts as playlist ([#428](https://github.com/OpenAudio/go-openaudio/issues/428)) ([26ccde7](https://github.com/OpenAudio/go-openaudio/commit/26ccde79181ff8c3dd1e711070adbb7327e6be68))
* **etl:** record migrated entities with their original created_at ([#439](https://github.com/OpenAudio/go-openaudio/issues/439)) ([a0aa0e8](https://github.com/OpenAudio/go-openaudio/commit/a0aa0e81975f4ef3074171b4305469f4a34ce7b1))
* **etl:** replay concluded remix contests through the migration ([#494](https://github.com/OpenAudio/go-openaudio/issues/494)) ([28a05c3](https://github.com/OpenAudio/go-openaudio/commit/28a05c3d86dbbfa532b1c15a882deffa5cefb380))
* **etl:** report transactions that match no handler ([#480](https://github.com/OpenAudio/go-openaudio/issues/480)) ([36c806c](https://github.com/OpenAudio/go-openaudio/commit/36c806ca96dedff61b647d6b3ff72d08ecdeef9e))
* **etl:** store the DDEX rights metadata tracks already send ([#461](https://github.com/OpenAudio/go-openaudio/issues/461)) ([c1c8993](https://github.com/OpenAudio/go-openaudio/commit/c1c899310c323c162fa152a9c0243954fd61b86e))
* **etl:** write playlist_tracks timestamps in block time ([#482](https://github.com/OpenAudio/go-openaudio/issues/482)) ([733c8b2](https://github.com/OpenAudio/go-openaudio/commit/733c8b26e4094682a9bffe293d625c4262092fb5))
* **genesis-writer:** emit email_owner_user_id so encrypted emails index ([#490](https://github.com/OpenAudio/go-openaudio/issues/490)) ([b7c3597](https://github.com/OpenAudio/go-openaudio/commit/b7c35979cf67dadde2df4c97837203ff578dfa25))
* **genesis-writer:** emit every timestamp in UTC ([#488](https://github.com/OpenAudio/go-openaudio/issues/488)) ([3a877e1](https://github.com/OpenAudio/go-openaudio/commit/3a877e14e292441bd1920594a504464d0f9b658c))
* **genesis-writer:** emit muted users under the User entity type ([#489](https://github.com/OpenAudio/go-openaudio/issues/489)) ([e590681](https://github.com/OpenAudio/go-openaudio/commit/e590681b6ae22c5b8dad9c26b6a7584ef7b72ae0))
* **genesis-writer:** emit six missing track state flags ([#486](https://github.com/OpenAudio/go-openaudio/issues/486)) ([3a4883a](https://github.com/OpenAudio/go-openaudio/commit/3a4883a6259d4599f548f6a6d90d362e862ff13e))
* **genesis-writer:** emit six state fields the indexer reads ([#491](https://github.com/OpenAudio/go-openaudio/issues/491)) ([a708bc1](https://github.com/OpenAudio/go-openaudio/commit/a708bc1988243f295abb9650cc27d4c988c41b4d))
* **genesis-writer:** migrate subscriptions to events ([#493](https://github.com/OpenAudio/go-openaudio/issues/493)) ([04ee231](https://github.com/OpenAudio/go-openaudio/commit/04ee2314b56ef06b8cdfadb3c756c4623a06bca7))
* **genesis:** carry soft-deleted state on Create instead of dropping the row ([#455](https://github.com/OpenAudio/go-openaudio/issues/455)) ([74e3a70](https://github.com/OpenAudio/go-openaudio/commit/74e3a70d160da6775648b1aef1604a29bf6d73c3))
* **genesis:** carry the entity fields the writer never put on Create ([#445](https://github.com/OpenAudio/go-openaudio/issues/445)) ([947c053](https://github.com/OpenAudio/go-openaudio/commit/947c053f1a181d7ee018d0f589ba61654d221f6a))
* **genesis:** emit comment reactions under the Comment entity type ([#444](https://github.com/OpenAudio/go-openaudio/issues/444)) ([28b9190](https://github.com/OpenAudio/go-openaudio/commit/28b9190f11b8d2c211d25d65eaaa6c56828a8db5))
* **genesis:** emit comment replies after their parents ([#441](https://github.com/OpenAudio/go-openaudio/issues/441)) ([68fd451](https://github.com/OpenAudio/go-openaudio/commit/68fd451bfe9c1c70b325b8c273dd08b0dfc18b03))
* **genesis:** emit missing entity metadata and sign apps/grants as the owning user ([#438](https://github.com/OpenAudio/go-openaudio/issues/438)) ([3e2e259](https://github.com/OpenAudio/go-openaudio/commit/3e2e2597410353f4d8fd036b3c435f69dd61e92c))
* **genesis:** emit root comments and replies in separate passes ([#472](https://github.com/OpenAudio/go-openaudio/issues/472)) ([b009158](https://github.com/OpenAudio/go-openaudio/commit/b0091586f9989110bc4ed77c8084d10824a521ee))
* **genesis:** keep each track's existing slug instead of regenerating it ([#452](https://github.com/OpenAudio/go-openaudio/issues/452)) ([49841ce](https://github.com/OpenAudio/go-openaudio/commit/49841cea93aa5f796820e32f52b64c4ce4f8d883))
* **genesis:** migrate soft-deleted wallet links by carrying is_delete on Create ([#442](https://github.com/OpenAudio/go-openaudio/issues/442)) ([cb5abe8](https://github.com/OpenAudio/go-openaudio/commit/cb5abe865c068758117177304fad794bb34c9b03))
* **genesis:** only emit transactions whose references resolve ([#449](https://github.com/OpenAudio/go-openaudio/issues/449)) ([da0256d](https://github.com/OpenAudio/go-openaudio/commit/da0256d51bf80a520d69a620c815e496dd34dd63))
* **genesis:** project consensus auth state during the genesis write ([#464](https://github.com/OpenAudio/go-openaudio/issues/464)) ([f8a8fa3](https://github.com/OpenAudio/go-openaudio/commit/f8a8fa38d179a215dca5a7f88007fcc4efb77023))
* **mediorum:** bound repair pulls, fall back to store-all peers, surface checkpoint age ([#436](https://github.com/OpenAudio/go-openaudio/issues/436)) ([56153da](https://github.com/OpenAudio/go-openaudio/commit/56153dabeeba6e433559c5860ec739bc554632e1))
* **mediorum:** harden media response headers ([#446](https://github.com/OpenAudio/go-openaudio/issues/446)) ([98273f7](https://github.com/OpenAudio/go-openaudio/commit/98273f783b238490546fc53c5fbf60eca83098b2))
* **mediorum:** match access authorities case-insensitively ([#203](https://github.com/OpenAudio/go-openaudio/issues/203)) ([acd150d](https://github.com/OpenAudio/go-openaudio/commit/acd150d65328e7bafaee0340112c26969e88c1f1))
* **mediorum:** reclaim orphaned .tmp files on write instead of walking at startup ([#435](https://github.com/OpenAudio/go-openaudio/issues/435)) ([10d1953](https://github.com/OpenAudio/go-openaudio/commit/10d1953d3204b3315394a8c0a4cc8150b59f66c3))
* **mediorum:** repair upload cursor and quadratic file-bucket listing ([#434](https://github.com/OpenAudio/go-openaudio/issues/434)) ([63cc6c6](https://github.com/OpenAudio/go-openaudio/commit/63cc6c6df0c6c52b40b79a3c352c12d8594c2816))
* **parity:** do not skip every row when the reference is a snapshot ([#475](https://github.com/OpenAudio/go-openaudio/issues/475)) ([72da008](https://github.com/OpenAudio/go-openaudio/commit/72da0082a267b98a6b01248cf59746252b024b1a))


### Performance Improvements

* **etl:** drop the per-transaction savepoint when replaying migration blocks ([#451](https://github.com/OpenAudio/go-openaudio/issues/451)) ([4639757](https://github.com/OpenAudio/go-openaudio/commit/46397573275d9208e58c83557534c2d47a996698))


### Code Refactoring

* remove crudr peer transport ([#366](https://github.com/OpenAudio/go-openaudio/issues/366)) ([2f80b66](https://github.com/OpenAudio/go-openaudio/commit/2f80b66aa533b2d6e1292a7bb11aef6442e43f3e))

## [1.8.2](https://github.com/OpenAudio/go-openaudio/compare/v1.8.1...v1.8.2) (2026-07-28)


### Bug Fixes

* **ci:** give release PRs component branches so tagging works ([#413](https://github.com/OpenAudio/go-openaudio/issues/413)) ([16ed1aa](https://github.com/OpenAudio/go-openaudio/commit/16ed1aad54fb0bb03769df409956d0f060ee9b52))
* **core:** Fix v2 finalization rollback on duplicate tx ([#385](https://github.com/OpenAudio/go-openaudio/issues/385)) ([16c2893](https://github.com/OpenAudio/go-openaudio/commit/16c2893ce04ac1f984422915feb371b0301b5410))
* **etl:** persist orig_filename and stop updates from unlinking stems ([#421](https://github.com/OpenAudio/go-openaudio/issues/421)) ([1d9f697](https://github.com/OpenAudio/go-openaudio/commit/1d9f69772e870ec920a2dcef3c12328947dbd57d))
* **mediorum:** enable core writes by default ([#422](https://github.com/OpenAudio/go-openaudio/issues/422)) ([d65dded](https://github.com/OpenAudio/go-openaudio/commit/d65dded51255c6377aad829a5293f29d6067bf12))
* **mediorum:** limit persisted transcode errors ([#416](https://github.com/OpenAudio/go-openaudio/issues/416)) ([20cc952](https://github.com/OpenAudio/go-openaudio/commit/20cc9524a7c04ef9f6646871136d200372a5bcf7))
* **mediorum:** mark legacy core backlog once ([#418](https://github.com/OpenAudio/go-openaudio/issues/418)) ([c81982c](https://github.com/OpenAudio/go-openaudio/commit/c81982c48a4435dc386d24d27088a5e5e731c04c))
* **mediorum:** reject oversized core operations ([#417](https://github.com/OpenAudio/go-openaudio/issues/417)) ([f9895f8](https://github.com/OpenAudio/go-openaudio/commit/f9895f87e3605fdadc0fe92582fea708dc5a11df))
* **mediorum:** skip legacy transient retry ops ([#419](https://github.com/OpenAudio/go-openaudio/issues/419)) ([77164ea](https://github.com/OpenAudio/go-openaudio/commit/77164eaf103f2d998498e86f9ca9864b79deed62))
* **mediorum:** stop terminal transcode retries ([#338](https://github.com/OpenAudio/go-openaudio/issues/338)) ([94b7638](https://github.com/OpenAudio/go-openaudio/commit/94b763854927c2c8167318cd916324efb54c8f1e))
* prevent log spam from flooding Axiom during outages ([#420](https://github.com/OpenAudio/go-openaudio/issues/420)) ([2440570](https://github.com/OpenAudio/go-openaudio/commit/2440570e3e4483a7c1b23f7f4f1f077bb6d50d59))

## [1.8.1](https://github.com/OpenAudio/go-openaudio/compare/v1.8.0...v1.8.1) (2026-07-17)


### Bug Fixes

* **mediorum:** default core writes off until fleet is capped ([#411](https://github.com/OpenAudio/go-openaudio/issues/411)) ([286fec5](https://github.com/OpenAudio/go-openaudio/commit/286fec5e88c8233ba9d6802f1d3cdecbc0c751ff))

## [1.8.0](https://github.com/OpenAudio/go-openaudio/compare/v1.7.1...v1.8.0) (2026-07-16)


### Features

* **console:** add consensus halt detection panel and banner ([#407](https://github.com/OpenAudio/go-openaudio/issues/407)) ([7f0550a](https://github.com/OpenAudio/go-openaudio/commit/7f0550ac9d9a2a2b4e47b16196b2e2956670fa15))


### Bug Fixes

* **ci:** let release-please tag root-only release PRs ([#408](https://github.com/OpenAudio/go-openaudio/issues/408)) ([0a86e3d](https://github.com/OpenAudio/go-openaudio/commit/0a86e3d92194b48fc86da2ae1ee6ed2a8a3ef944))
* **core:** respect MaxTxBytes when preparing proposals ([#404](https://github.com/OpenAudio/go-openaudio/issues/404)) ([242dc93](https://github.com/OpenAudio/go-openaudio/commit/242dc93f6ae036e4bdc8564ca3fbeedd5a573d1a))
* **etl:** don't let track updates wipe CIDs via explicit null ([#410](https://github.com/OpenAudio/go-openaudio/issues/410)) ([b4a5ebe](https://github.com/OpenAudio/go-openaudio/commit/b4a5ebe0698ff11ff3fdaf57de3f3aa881d704e7))

## [1.7.1](https://github.com/OpenAudio/go-openaudio/compare/v1.7.0...v1.7.1) (2026-07-14)


### Bug Fixes

* **mediorum:** document core-writes default and restore release trigger ([#405](https://github.com/OpenAudio/go-openaudio/issues/405)) ([d0bd006](https://github.com/OpenAudio/go-openaudio/commit/d0bd0060540810ee283111ec74f8ba7ef527dd8f))

## [1.7.0](https://github.com/OpenAudio/go-openaudio/compare/v1.6.0...v1.7.0) (2026-07-13)


### Features

* enable mediorum core writes ([#365](https://github.com/OpenAudio/go-openaudio/issues/365)) ([f370728](https://github.com/OpenAudio/go-openaudio/commit/f3707281cc1081af32dd3a8b642899257e07527f))


### Bug Fixes

* **db:** cap postgres connection pools ([#359](https://github.com/OpenAudio/go-openaudio/issues/359)) ([b7c8a4a](https://github.com/OpenAudio/go-openaudio/commit/b7c8a4a8e67f79605607455460b8253aa9d4fc83))
* **etl:** restore migration 0034 stub ([#401](https://github.com/OpenAudio/go-openaudio/issues/401)) ([50e54b5](https://github.com/OpenAudio/go-openaudio/commit/50e54b5c236ed71437c3b379f6f5fd05b8e0af36))
* guard state sync snapshots by disk space ([#400](https://github.com/OpenAudio/go-openaudio/issues/400)) ([cd874bc](https://github.com/OpenAudio/go-openaudio/commit/cd874bcc37b81ecb9794830c03cb5690462ea979))
* make duplicate reward pool creates idempotent ([#374](https://github.com/OpenAudio/go-openaudio/issues/374)) ([5527ff8](https://github.com/OpenAudio/go-openaudio/commit/5527ff884f8cc000c929a46dca6caeda85eabc69))
* reject duplicate ERN entity refs ([#383](https://github.com/OpenAudio/go-openaudio/issues/383)) ([6d42ce9](https://github.com/OpenAudio/go-openaudio/commit/6d42ce9108199b5bbb3b33e7a35b2df7abcc0cdb))
* route eth reads over http ([#396](https://github.com/OpenAudio/go-openaudio/issues/396)) ([17dcef7](https://github.com/OpenAudio/go-openaudio/commit/17dcef7d6148de9ad6da1e4ba76a66c8066a8651))

## [1.6.0](https://github.com/OpenAudio/go-openaudio/compare/v1.5.0...v1.6.0) (2026-07-01)


### Features

* **etl:** support ManageEntityMigration transactions ([#375](https://github.com/OpenAudio/go-openaudio/issues/375)) ([dfabacb](https://github.com/OpenAudio/go-openaudio/commit/dfabacb346d9ff3945421b06a21db7b11cda76c1))
* **genesis-writer:** add offline chain history population tool ([#210](https://github.com/OpenAudio/go-openaudio/issues/210)) ([d730e51](https://github.com/OpenAudio/go-openaudio/commit/d730e511f763aae9277483abde2a66e40225fa4f))
* **genesis-writer:** add play count reconciliation step ([#376](https://github.com/OpenAudio/go-openaudio/issues/376)) ([2a30516](https://github.com/OpenAudio/go-openaudio/commit/2a30516cdbdb6de3fad0792b1604ff43a2564fb8))
* **genesis-writer:** add track collaborator migration ([#381](https://github.com/OpenAudio/go-openaudio/issues/381)) ([043923e](https://github.com/OpenAudio/go-openaudio/commit/043923eab95b87f66984970cc5a47bc362536f63))
* read mediorum ops from core ([#364](https://github.com/OpenAudio/go-openaudio/issues/364)) ([20e6ec8](https://github.com/OpenAudio/go-openaudio/commit/20e6ec8f9cdcc444a0fe4154a1624e92ee1bfd44))


### Bug Fixes

* **genesis-writer:** migrate access_authorities for programmable distribution ([#377](https://github.com/OpenAudio/go-openaudio/issues/377)) ([aea1d66](https://github.com/OpenAudio/go-openaudio/commit/aea1d6672dada6a932964945e4412f3ddefb3413))
* persist user deactivation updates ([#394](https://github.com/OpenAudio/go-openaudio/issues/394)) ([b75c8bb](https://github.com/OpenAudio/go-openaudio/commit/b75c8bb6f8c5c6e58175894d84cc72a2462aaae8))
* skip validator updates for noop registrations ([#372](https://github.com/OpenAudio/go-openaudio/issues/372)) ([7ee965c](https://github.com/OpenAudio/go-openaudio/commit/7ee965c682108053b755698ac4569e721ebbfbd9))


### Reverts

* remove event_routes table and slug handling ([#386](https://github.com/OpenAudio/go-openaudio/issues/386)) ([ca62eef](https://github.com/OpenAudio/go-openaudio/commit/ca62eefc1bb44fce0e311ea0f74030ea03b3236b))

## [1.5.0](https://github.com/OpenAudio/go-openaudio/compare/v1.4.0...v1.5.0) (2026-06-18)


### Features

* **etl:** normalize genre on entity_manager track writes ([#367](https://github.com/OpenAudio/go-openaudio/issues/367)) ([5aa118b](https://github.com/OpenAudio/go-openaudio/commit/5aa118b67bc19474a56f81a6665f2f47854d72e4))


### Bug Fixes

* skip comet removal for jailed deregistrations ([#370](https://github.com/OpenAudio/go-openaudio/issues/370)) ([3c178ed](https://github.com/OpenAudio/go-openaudio/commit/3c178edc3dea729f85980b0deeba7dcecdb19a5a))

## [1.4.0](https://github.com/OpenAudio/go-openaudio/compare/v1.3.0...v1.4.0) (2026-06-15)


### Features

* **console:** show registry diffs on validators page ([#306](https://github.com/OpenAudio/go-openaudio/issues/306)) ([f30302f](https://github.com/OpenAudio/go-openaudio/commit/f30302ff38ec3b5b9e559472c6e659b1752f4872))
* **console:** show store-all node status ([#352](https://github.com/OpenAudio/go-openaudio/issues/352)) ([d4c3e92](https://github.com/OpenAudio/go-openaudio/commit/d4c3e9273aa6c67db7a92375f1228521b2c192e1))
* **console:** surface jailed status and stable height ([#326](https://github.com/OpenAudio/go-openaudio/issues/326)) ([f6e02dd](https://github.com/OpenAudio/go-openaudio/commit/f6e02dd0f3d864997eeaf53ef04f65f78e78a99d))
* **core/db:** drop unused core indexes ([#270](https://github.com/OpenAudio/go-openaudio/issues/270)) ([361cd0c](https://github.com/OpenAudio/go-openaudio/commit/361cd0cb85786d2183fdabb54c8e6d9564adb086))
* **core:** self-heal registration if node falls out of core_validators ([#309](https://github.com/OpenAudio/go-openaudio/issues/309)) ([7c33778](https://github.com/OpenAudio/go-openaudio/commit/7c33778dcaeed144e6478ffcfaf37d9a8695a0fc))
* **etl:** add event_routes for contest permalink support ([#354](https://github.com/OpenAudio/go-openaudio/issues/354)) ([97ccec3](https://github.com/OpenAudio/go-openaudio/commit/97ccec32c1c62f6bd0f8c0aee68191cafbad6523))
* **etl:** add TrackCollaborator to the entity manager spec ([#345](https://github.com/OpenAudio/go-openaudio/issues/345)) ([550356d](https://github.com/OpenAudio/go-openaudio/commit/550356d6fdf88301678fccf59ee0e2a2562c0488))
* **etl:** allowed_api_keys + access_authorities normalization (parity 5B) ([#250](https://github.com/OpenAudio/go-openaudio/issues/250)) ([247007a](https://github.com/OpenAudio/go-openaudio/commit/247007a881984cff9a29320a66ac39863225b0a5))
* **etl:** auto-subscribe uploader to remix-contest event on Track Create ([#311](https://github.com/OpenAudio/go-openaudio/issues/311)) ([1b70465](https://github.com/OpenAudio/go-openaudio/commit/1b70465de8b7d45ccec45e9d38d04e34a9a5e2f6))
* **etl:** comments.is_members_only + video_url for fan club text posts ([#312](https://github.com/OpenAudio/go-openaudio/issues/312)) ([3b99954](https://github.com/OpenAudio/go-openaudio/commit/3b99954f85383b3066a10c95b7312ad7653bcae6))
* **etl:** consume blocks via gRPC StreamBlocks with catch-up + fallback ([#342](https://github.com/OpenAudio/go-openaudio/issues/342)) ([aee578e](https://github.com/OpenAudio/go-openaudio/commit/aee578ec923b789a8c97b361642659a458473313))
* **etl:** decode hashid-encoded track_ids in playlist_contents (parity 5A) ([#269](https://github.com/OpenAudio/go-openaudio/issues/269)) ([c8fddb6](https://github.com/OpenAudio/go-openaudio/commit/c8fddb6fb9a51db7baf7fa5f00b8e7adb79e88dd))
* **etl:** index oauth_redirect_uris on developer app create/update (parity 5E) ([#252](https://github.com/OpenAudio/go-openaudio/issues/252)) ([bc86dea](https://github.com/OpenAudio/go-openaudio/commit/bc86deab948b20673c3bfa315a469592344f0d68))
* **etl:** let a collaborator leave a track after accepting ([#349](https://github.com/OpenAudio/go-openaudio/issues/349)) ([22066c3](https://github.com/OpenAudio/go-openaudio/commit/22066c3521d3f6d60578e7f879899689bbc6b58d))
* **etl:** post-handler hooks for User/Track/Playlist Create + generic registry ([#317](https://github.com/OpenAudio/go-openaudio/issues/317)) ([1c75995](https://github.com/OpenAudio/go-openaudio/commit/1c7599596a27e376d7e374a7543b8a5abe826a76))
* **etl:** post-write hook for Plays transactions ([#322](https://github.com/OpenAudio/go-openaudio/issues/322)) ([4d1c9df](https://github.com/OpenAudio/go-openaudio/commit/4d1c9dfdfb529bad111e03a3d10221c25b48677f))
* **examples:** local ETL harness for stream/poll + tx load + resume ([#344](https://github.com/OpenAudio/go-openaudio/issues/344)) ([7758ae7](https://github.com/OpenAudio/go-openaudio/commit/7758ae709d18e933d817f3e2c20e420a71b0c89d))
* **mediorum:** flip health-check unhealthy when bucket writes fail ([#297](https://github.com/OpenAudio/go-openaudio/issues/297)) ([94793f9](https://github.com/OpenAudio/go-openaudio/commit/94793f91594f81f80421e3e00668552e8c1f9af8))
* **mediorum:** prune the append-only crudr ops log ([#325](https://github.com/OpenAudio/go-openaudio/issues/325)) ([f850a6b](https://github.com/OpenAudio/go-openaudio/commit/f850a6b235e69a0acf026f3c6af09107ad29528a))
* **mediorum:** retain recent storage repairs ([#353](https://github.com/OpenAudio/go-openaudio/issues/353)) ([cd10354](https://github.com/OpenAudio/go-openaudio/commit/cd10354e6bfe17eb59743110bb92c848204949ac))


### Bug Fixes

* **ci:** wait for SHA-tagged image before retagging release tag ([#291](https://github.com/OpenAudio/go-openaudio/issues/291)) ([cc875ef](https://github.com/OpenAudio/go-openaudio/commit/cc875ef5459d6cca28a112cfc2694f6b6fe6ee88))
* **console:** rate limit historical pages ([#358](https://github.com/OpenAudio/go-openaudio/issues/358)) ([919dbb3](https://github.com/OpenAudio/go-openaudio/commit/919dbb36f69daacc0767be37aa963647239b088e))
* **console:** separate archive and store-all indicators ([#355](https://github.com/OpenAudio/go-openaudio/issues/355)) ([a33e361](https://github.com/OpenAudio/go-openaudio/commit/a33e361e56c621d216a902c290f1a732855d4f0a))
* **console:** update jailed banner copy ([#328](https://github.com/OpenAudio/go-openaudio/issues/328)) ([6b2b7ee](https://github.com/OpenAudio/go-openaudio/commit/6b2b7ee765fb7922539739775152de855a353d20))
* **core:** broaden mainnet p2p bootstrap peers ([#361](https://github.com/OpenAudio/go-openaudio/issues/361)) ([3bdbf51](https://github.com/OpenAudio/go-openaudio/commit/3bdbf51ed78ebdf49bb1b39c54958560549bd0a5))
* **core:** coerce nil ProverAddresses to empty slice in finalizeStorageProof ([#313](https://github.com/OpenAudio/go-openaudio/issues/313)) ([c786d3f](https://github.com/OpenAudio/go-openaudio/commit/c786d3f8dfa1c14e3129793f4e0ed821f6de1901))
* **core:** guard against empty ProverAddresses in PoS submission and validation ([#315](https://github.com/OpenAudio/go-openaudio/issues/315)) ([983bde8](https://github.com/OpenAudio/go-openaudio/commit/983bde8f5ff4653a62c9076297d7ba44fd2fa181))
* **core:** keep storage proof checks out of block replay ([#362](https://github.com/OpenAudio/go-openaudio/issues/362)) ([8d55ef6](https://github.com/OpenAudio/go-openaudio/commit/8d55ef6b33298bf8531e26db40217d3e6ab2872f))
* **core:** pass --disable-triggers to pg_restore data section ([#293](https://github.com/OpenAudio/go-openaudio/issues/293)) ([438c29f](https://github.com/OpenAudio/go-openaudio/commit/438c29f52683d823f751a003360b2439e86b6c06))
* **core:** regenerate stale priv_validator_key.json when delegate key rotates ([#299](https://github.com/OpenAudio/go-openaudio/issues/299)) ([5a2e0cd](https://github.com/OpenAudio/go-openaudio/commit/5a2e0cdf639820ae4de1cfc290dcad8aaf810d27))
* **core:** validate StorageProof structure in CheckTx ([#316](https://github.com/OpenAudio/go-openaudio/issues/316)) ([88444d5](https://github.com/OpenAudio/go-openaudio/commit/88444d51a5b0b966a819f9fb22e9648766ee11cc))
* **etl:** canonicalize playlist_contents entry keys on write ([#321](https://github.com/OpenAudio/go-openaudio/issues/321)) ([35ad422](https://github.com/OpenAudio/go-openaudio/commit/35ad422c0f2753c33f9b709dc91bcab626aac9a9))
* **etl:** comment threading guards (parity 5C) ([#251](https://github.com/OpenAudio/go-openaudio/issues/251)) ([56db895](https://github.com/OpenAudio/go-openaudio/commit/56db89571b49d79008885fc01cb81daac7c8f3ce))
* **etl:** count runes not bytes in name/bio/handle/description limits ([#340](https://github.com/OpenAudio/go-openaudio/issues/340)) ([7062cd9](https://github.com/OpenAudio/go-openaudio/commit/7062cd90dff56af681d738cad9c5cee2958e0445))
* **etl:** default release_date to created_at when unset (python parity) ([#333](https://github.com/OpenAudio/go-openaudio/issues/333)) ([959f1c7](https://github.com/OpenAudio/go-openaudio/commit/959f1c7db8e05178177afccd2f8bd2ada2ce3080))
* **etl:** drop incompatible developer_apps UNIQUE(address) + auto-seed block in tests ([#302](https://github.com/OpenAudio/go-openaudio/issues/302)) ([6885f9a](https://github.com/OpenAudio/go-openaudio/commit/6885f9a011f3df661086ad20ac330fb5f46d1a7b))
* **etl:** four prod-clone-run bugs + [#307](https://github.com/OpenAudio/go-openaudio/issues/307) test fixes folded in ([#308](https://github.com/OpenAudio/go-openaudio/issues/308)) ([4e0c89b](https://github.com/OpenAudio/go-openaudio/commit/4e0c89bce96b3043e7d6c99306438dafd8a7bb42))
* **etl:** halt on block indexing failure ([#323](https://github.com/OpenAudio/go-openaudio/issues/323)) ([819100b](https://github.com/OpenAudio/go-openaudio/commit/819100b28c94215b609c02f97c7338738bc1d4f1))
* **etl:** let a grant's grantee revoke their own user-to-user grant ([#360](https://github.com/OpenAudio/go-openaudio/issues/360)) ([5edfdf3](https://github.com/OpenAudio/go-openaudio/commit/5edfdf36a895d1a8a33e2416fffd3d30cac498e5))
* **etl:** normalize empty playlist_contents on update (apps[#14306](https://github.com/OpenAudio/go-openaudio/issues/14306) parity) ([#265](https://github.com/OpenAudio/go-openaudio/issues/265)) ([a8016e9](https://github.com/OpenAudio/go-openaudio/commit/a8016e9e1661ec5344d3d0d29a1346f7dd190c69))
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
* **mediorum:** avoid blocked replication pipe goroutines ([#336](https://github.com/OpenAudio/go-openaudio/issues/336)) ([8b7cd74](https://github.com/OpenAudio/go-openaudio/commit/8b7cd74ad3ba39b2f980ef60479976b8fb4b66f7))
* **mediorum:** bound audio analysis backlog scans ([#274](https://github.com/OpenAudio/go-openaudio/issues/274)) ([689709e](https://github.com/OpenAudio/go-openaudio/commit/689709edc3774b6c2f0069186749d23b902515bd))


### Performance Improvements

* **etl:** index route (owner_id, title_slug) for slug-collision lookups ([#337](https://github.com/OpenAudio/go-openaudio/issues/337)) ([43606e8](https://github.com/OpenAudio/go-openaudio/commit/43606e8d8e5aac580b9665c4deefdcfac0852cae))
* **etl:** stop maintaining blocks is_current ([#330](https://github.com/OpenAudio/go-openaudio/issues/330)) ([ea6c925](https://github.com/OpenAudio/go-openaudio/commit/ea6c925d2455b7615c3070ed455d743686de21a0))
* **etl:** upsert social writes in place instead of demote-then-insert ([#331](https://github.com/OpenAudio/go-openaudio/issues/331)) ([24084d2](https://github.com/OpenAudio/go-openaudio/commit/24084d2bf30baaef46baed6728d5c65df4709850))


### Code Refactoring

* **etl:** tighten track_collaborators reconcile ([#346](https://github.com/OpenAudio/go-openaudio/issues/346)) ([3904b9d](https://github.com/OpenAudio/go-openaudio/commit/3904b9d7046c9fa453d201736907709bc7469a13))

## [1.3.0](https://github.com/OpenAudio/go-openaudio/compare/v1.2.14...v1.3.0) (2026-05-19)


### Features

* **console:** surface archive storage stats with per-bucket disk indicators ([#282](https://github.com/OpenAudio/go-openaudio/issues/282)) ([1eb8a38](https://github.com/OpenAudio/go-openaudio/commit/1eb8a389f5c343831b08c28aa9379b8c2cc05f7d))
* **core/console:** Storage tab polish (truncate long tables, Files Changed col) ([#279](https://github.com/OpenAudio/go-openaudio/issues/279)) ([356ebfc](https://github.com/OpenAudio/go-openaudio/commit/356ebfc428a0a42e3a497c0b0331aa1f94b5b609))
* **etl:** enforce immutable field set on track/playlist update (parity 2D) ([#245](https://github.com/OpenAudio/go-openaudio/issues/245)) ([6a2be4b](https://github.com/OpenAudio/go-openaudio/commit/6a2be4bd82c396483e42483b9197d591aad7b78e))
* **etl:** scheduled release publisher (parity 4A) ([#247](https://github.com/OpenAudio/go-openaudio/issues/247)) ([e8adcb9](https://github.com/OpenAudio/go-openaudio/commit/e8adcb92d19f060c4128b07722781d05455b0ede))
* **etl:** track_price_history + album_price_history tables and writes ([#243](https://github.com/OpenAudio/go-openaudio/issues/243)) ([52b771a](https://github.com/OpenAudio/go-openaudio/commit/52b771a49c63907d8f7d784bf927a63809b9a80b))
* **mediorum:** optional parallelism for repair runs ([#281](https://github.com/OpenAudio/go-openaudio/issues/281)) ([a23df44](https://github.com/OpenAudio/go-openaudio/commit/a23df44d8b1ab5a527163f722b56f80271fcfa37))
* **mediorum:** read-repair on failed PoS challenge ([#280](https://github.com/OpenAudio/go-openaudio/issues/280)) ([c9e8926](https://github.com/OpenAudio/go-openaudio/commit/c9e8926bc5ee7d33d3d0e5b16c2b1752ed2ce552))


### Bug Fixes

* **ci:** rename release-please config to expected filename ([#286](https://github.com/OpenAudio/go-openaudio/issues/286)) ([c7dc98b](https://github.com/OpenAudio/go-openaudio/commit/c7dc98b4341236fd2ed3f0f19d6014f31d212768))
* **ci:** use App client-id (not app-id) for release-please auth ([#290](https://github.com/OpenAudio/go-openaudio/issues/290)) ([4d2e6aa](https://github.com/OpenAudio/go-openaudio/commit/4d2e6aa037f3adc3e2b95c33f76950327a86f28f))
* **etl:** restore 0017 stub so golang-migrate can step past version 17 ([#283](https://github.com/OpenAudio/go-openaudio/issues/283)) ([17efa20](https://github.com/OpenAudio/go-openaudio/commit/17efa201098353a35e2f49b7be6e8bf0dc381cac))
* **mediorum:** suppress no-op uploads ops on replication retries ([#278](https://github.com/OpenAudio/go-openaudio/issues/278)) ([2a4c77a](https://github.com/OpenAudio/go-openaudio/commit/2a4c77a4d4ca9d5a0ff8faff0d966ed1f7f5fc62))
* trigger initial release-please dry-run ([#285](https://github.com/OpenAudio/go-openaudio/issues/285)) ([0841e17](https://github.com/OpenAudio/go-openaudio/commit/0841e173300a6e15caee74e78fde01e6d20b2b38))
