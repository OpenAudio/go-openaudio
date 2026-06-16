# Changelog

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
