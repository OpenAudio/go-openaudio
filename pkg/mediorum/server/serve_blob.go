package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	"github.com/OpenAudio/go-openaudio/pkg/registrar"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"

	"github.com/erni27/imcache"
	"github.com/labstack/echo/v4"
	"gocloud.dev/gcerrors"
	"golang.org/x/exp/slices"
)

func (ss *MediorumServer) serveBlobLocation(c echo.Context) error {
	ctx := c.Request().Context()
	cid := c.Param("cid")
	preferred, _ := ss.rendezvousAllHosts(cid)

	// if ?sniff=1 to actually find the hosts that have it
	sniff, _ := strconv.ParseBool(c.QueryParam("sniff"))
	var attrs []HostAttrSniff
	if sniff {
		fix, _ := strconv.ParseBool(c.QueryParam("fix"))
		attrs = ss.sniffAndFix(ctx, cid, fix)
	}

	return c.JSON(200, map[string]any{
		"cid":       cid,
		"preferred": preferred,
		"sniff":     attrs,
	})
}

func (ss *MediorumServer) serveBlobInfo(c echo.Context) error {
	ctx := c.Request().Context()
	cid := c.Param("cid")
	key := cidutil.ShardCID(cid)
	attr, err := ss.bucket.Attributes(ctx, key)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			return c.String(404, "blob not found")
		}
		ss.logger.Warn("error getting blob attributes", zap.Error(err))
		return err
	}

	// since this is called before redirecting, make sure this node can actually serve the blob (it needs to check db for delisted status)
	dbHealthy := ss.databaseSize > 0 && ss.dbSizeErr == "" && ss.uploadsCountErr == ""
	if !dbHealthy {
		return c.String(500, "database connection issue")
	}

	return c.JSON(200, attr)
}

func (ss *MediorumServer) ensureNotDelisted(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx := c.Request().Context()
		key := c.Param("cid")

		if ss.isCidBlacklisted(ctx, key) {
			ss.logger.Debug("cid is blacklisted", zap.String("cid", key))
			return c.String(403, "cid is blacklisted by this node")
		}

		c.Set("checkedDelistStatus", true)
		return next(c)
	}
}

func (ss *MediorumServer) serveBlob(c echo.Context) error {
	ctx := c.Request().Context()
	cid := c.Param("cid")

	// the only keys we store with ".jpg" suffixes are of the format "<cid>/<size>.jpg", so remove the ".jpg" if it's just like "<cid>.jpg"
	// this is to support clients that forget to leave off the .jpg for this legacy format
	if strings.HasSuffix(cid, ".jpg") && !strings.Contains(cid, "/") {
		cid = cid[:len(cid)-4]

		// find and replace cid parameter for future calls
		names := c.ParamNames()
		values := c.ParamValues()
		for i, name := range names {
			if name == "cid" {
				values[i] = cid
			}
		}

		// set parameters back to the context
		c.SetParamNames(names...)
		c.SetParamValues(values...)
	}

	key := cidutil.ShardCID(cid)

	// if the client provided a filename, set it in the header to be auto-populated in download prompt
	filenameForDownload := c.QueryParam("filename")
	if filenameForDownload != "" {
		contentDisposition := mime.QEncoding.Encode("utf-8", filenameForDownload)
		c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, contentDisposition))
	}

	blob, err := ss.bucket.NewReader(ctx, key, nil)

	// If our bucket doesn't have the file, find a different node
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			// don't redirect if the client only wants to know if we have it (ie localOnly query param is true)
			if localOnly, _ := strconv.ParseBool(c.QueryParam("localOnly")); localOnly {
				return c.String(404, "blob not found")
			}

			// redirect to it
			host := ss.findNodeToServeBlob(ctx, cid)
			if host == "" {
				return c.String(404, "blob not found")
			}

			dest := ss.replaceHost(c, host)
			query := dest.Query()
			query.Add("allow_unhealthy", "true") // we confirmed the node has it, so allow it to serve it even if unhealthy
			dest.RawQuery = query.Encode()
			return c.Redirect(302, dest.String())
		}
		return err
	}

	defer func() {
		if blob != nil {
			blob.Close()
		}
	}()

	if c.Request().Method == "HEAD" {
		return c.NoContent(200)
	}

	isAudioFile := strings.HasPrefix(blob.ContentType(), "audio")

	if isAudioFile {
		// detect mime type and block mp3 streaming outside of the /tracks/cidstream route
		if !strings.Contains(c.Path(), "cidstream") {
			return c.String(401, "mp3 streaming is blocked. Please use Discovery /v1/tracks/:encodedId/stream")
		}
		// track metrics in separate threads
		go ss.recordMetric(StreamTrack)
		// synchronously write track listen to event queue
		ss.logTrackListen(c)
		setTimingHeader(c)

		if id3, _ := strconv.ParseBool(c.QueryParam("id3")); id3 {
			title := c.QueryParam("id3_title")
			artist := c.QueryParam("id3_artist")

			tag := buildID3v2Tag(title, artist)

			tagged := &taggedStream{
				tag:  tag,
				blob: blob,
			}

			// Rewind blob to start
			if _, err := blob.Seek(0, io.SeekStart); err != nil {
				return err
			}

			http.ServeContent(c.Response(), c.Request(), cid, blob.ModTime(), &struct {
				io.ReadSeeker
			}{
				ReadSeeker: tagged,
			})
			return nil
		}

		// stream audio
		http.ServeContent(c.Response(), c.Request(), cid, blob.ModTime(), blob)
		return nil
	} else {
		// non audio (images)
		// images: cache 30 days
		c.Response().Header().Set(echo.HeaderCacheControl, "public, max-age=2592000, immutable")
		blobData, err := io.ReadAll(blob)
		if err != nil {
			return err
		}
		go ss.recordMetric(ServeImage)
		return c.Blob(200, blob.ContentType(), blobData)
	}

}

func (ss *MediorumServer) recordMetric(action string) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	firstOfMonth := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)

	// Increment daily metric
	err := ss.crud.DB.Transaction(func(tx *gorm.DB) error {
		var metric DailyMetrics
		if err := tx.FirstOrCreate(&metric, DailyMetrics{
			Timestamp: today,
			Action:    action,
		}).Error; err != nil {
			return err
		}
		metric.Count += 1
		if err := tx.Save(&metric).Error; err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		ss.logger.Error("unable to increment daily metric", zap.Error(err), zap.String("action", action))
	}

	// Increment monthly metric
	err = ss.crud.DB.Transaction(func(tx *gorm.DB) error {
		var metric MonthlyMetrics
		if err := tx.FirstOrCreate(&metric, MonthlyMetrics{
			Timestamp: firstOfMonth,
			Action:    action,
		}).Error; err != nil {
			return err
		}
		metric.Count += 1
		if err := tx.Save(&metric).Error; err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		ss.logger.Error("unable to increment monthly metric", zap.Error(err), zap.String("action", action))
	}
}

func (ss *MediorumServer) findNodeToServeBlob(_ context.Context, key string) string {

	// use cache if possible
	if host, ok := ss.redirectCache.Get(key); ok {
		// verify host is all good
		if ss.hostHasBlob(host, key) {
			return host
		} else {
			ss.redirectCache.Remove(key)
		}
	}

	// try hosts to find blob
	hosts, _ := ss.rendezvousAllHosts(key)
	for _, h := range hosts {
		if ss.hostHasBlob(h, key) {
			ss.redirectCache.Set(key, h, imcache.WithDefaultExpiration())
			return h
		}
	}

	return ""
}

func (ss *MediorumServer) findAndPullBlob(ctx context.Context, key string) (string, error) {
	// start := time.Now()

	hosts, _ := ss.rendezvousAllHosts(key)
	for _, host := range hosts {
		err := ss.pullFileFromHost(ctx, host, key)
		if err == nil {
			return host, nil
		}
	}

	return "", errors.New("no host found with " + key)
}

func (ss *MediorumServer) logTrackListen(c echo.Context) {
	skipPlayCount, _ := strconv.ParseBool(c.QueryParam("skip_play_count"))
	if skipPlayCount {
		return
	}

	sig, err := signature.ParseFromQueryString(c.QueryParam("signature"))
	if err != nil {
		ss.logger.Warn(
			"unable to parse signature for request",
			zap.String("signature", c.QueryParam("signature")),
			zap.String("remote_addr", c.Request().RemoteAddr),
			zap.String("url", c.Request().URL.String()),
		)
		return
	}

	// as per CN `userId: req.userId ?? delegateOwnerWallet`
	userId := ss.Config.Self.Wallet
	if sig.Data.UserID != 0 {
		userId = strconv.Itoa(sig.Data.UserID)
	}

	signatureData, err := signature.GenerateListenTimestampAndSignature(ss.Config.privateKey)
	if err != nil {
		ss.logger.Error("unable to build request", zap.Error(err))
		return
	}

	// parse out time as proto object from legacy listen sig
	parsedTime, err := time.Parse(time.RFC3339, signatureData.Timestamp)
	if err != nil {
		ss.logger.Error("core error parsing time:", zap.Error(err))
		return
	}

	geoData, err := ss.getGeoFromIP(c.RealIP())
	if err != nil {
		ss.logger.Error("core plays bad ip", zap.Error(err))
		return
	}

	trackID := fmt.Sprint(sig.Data.TrackId)

	ss.playEventQueue.pushPlayEvent(&PlayEvent{
		UserID:           userId,
		TrackID:          trackID,
		PlayTime:         parsedTime,
		Signature:        signatureData.Signature,
		City:             geoData.City,
		Country:          geoData.Country,
		Region:           geoData.Region,
		RequestSignature: c.QueryParam("signature"),
	})

	ss.logger.Info("play logged", zap.String("user_id", userId), zap.String("track_id", trackID))
}

// checks signature from discovery node
// used for cidstream endpoint + gated content and audio analysis post endpoints
// based on: https://github.com/AudiusProject/audius-protocol/blob/main/creator-node/src/middlewares/contentAccess/contentAccessMiddleware.ts
func (s *MediorumServer) requireRegisteredSignature(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		cid := c.Param("cid")
		uploadID := c.Param("id")
		sig, err := signature.ParseFromQueryString(c.QueryParam("signature"))
		if err != nil {
			return c.JSON(401, map[string]string{
				"error":  "invalid signature",
				"detail": err.Error(),
			})
		} else {
			// check it was signed by a registered node / mediorum peer
			isRegistered := slices.ContainsFunc(s.Config.Signers, func(peer registrar.Peer) bool {
				return strings.EqualFold(peer.Wallet, sig.SignerWallet)
			}) || slices.ContainsFunc(s.Config.Peers, func(peer registrar.Peer) bool {
				return strings.EqualFold(peer.Wallet, sig.SignerWallet)
			})

			wallets := make([]string, len(s.Config.Signers)+len(s.Config.Peers))
			for i, peer := range s.Config.Signers {
				wallets[i] = peer.Wallet
			}
			for i, peer := range s.Config.Peers {
				wallets[len(s.Config.Signers)+i] = peer.Wallet
			}

			if !isRegistered {
				s.logger.Debug("sig no match", zap.String("signed by", sig.SignerWallet))
				return c.JSON(401, map[string]string{
					"error":         "signer not in list of registered nodes",
					"detail":        "signed by: " + sig.SignerWallet,
					"valid_signers": strings.Join(wallets, ","),
				})
			}

			// check signature not too old
			age := time.Since(time.Unix(sig.Data.Timestamp/1000, 0))
			if age > (time.Hour * 48) {
				return c.JSON(401, map[string]string{
					"error":  "signature too old",
					"detail": age.String(),
				})
			}

			// check it is for this cid
			if sig.Data.Cid != cid {
				return c.JSON(401, map[string]string{
					"error":  "signature contains incorrect CID",
					"detail": fmt.Sprintf("url: %s, signature %s", cid, sig.Data.Cid),
				})
			}

			// check it is for this upload
			if sig.Data.UploadID != uploadID {
				return c.JSON(401, map[string]string{
					"error":  "signature contains incorrect upload ID",
					"detail": fmt.Sprintf("url: %s, signature %s", uploadID, sig.Data.UploadID),
				})
			}

			// OK
			c.Response().Header().Set("x-signature-debug", sig.String())
		}

		return next(c)
	}
}

func (ss *MediorumServer) serveInternalBlobGET(c echo.Context) error {
	ctx := c.Request().Context()
	cid := c.Param("cid")
	key := cidutil.ShardCID(cid)

	blob, err := ss.bucket.NewReader(ctx, key, nil)
	if err != nil {
		return err
	}
	defer blob.Close()

	return c.Stream(200, blob.ContentType(), blob)
}

func (ss *MediorumServer) serveInternalBlobPOST(c echo.Context) error {
	if !ss.diskHasSpace() {
		return c.String(http.StatusServiceUnavailable, "disk is too full to accept new blobs")
	}

	form, err := c.MultipartForm()
	if err != nil {
		return err
	}
	files := form.File[filesFormFieldName]
	defer form.RemoveAll()

	for _, upload := range files {
		cid := upload.Filename
		logger := ss.logger.With(zap.String("cid", cid))

		inp, err := upload.Open()
		if err != nil {
			return err
		}
		defer inp.Close()

		err = cidutil.ValidateCID(cid, inp)
		if err != nil {
			logger.Error("postBlob got invalid CID", zap.Error(err))
			return c.JSON(400, map[string]string{
				"error": err.Error(),
			})
		}

		err = ss.replicateToMyBucket(c.Request().Context(), cid, inp)
		if err != nil {
			ss.logger.Error("accept ERR", zap.Error(err))
			return err
		}
	}

	return c.JSON(200, "ok")
}

func (ss *MediorumServer) serveLegacyBlobAnalysis(c echo.Context) error {
	cid := c.Param("cid")
	var analysis *QmAudioAnalysis
	err := ss.crud.DB.First(&analysis, "cid = ?", cid).Error
	if err != nil {
		return echo.NewHTTPError(404, err.Error())
	}
	return c.JSON(200, analysis)
}

func (ss *MediorumServer) serveTrack(c echo.Context) error {
	if ss.Config.Env != "dev" {
		return c.String(404, "not found")
	}

	trackId := c.Param("trackId")
	ctx := c.Request().Context()
	sig, err := signature.ParseFromQueryString(c.QueryParam("signature"))
	if err != nil {
		return c.JSON(401, map[string]string{
			"error":  "invalid signature",
			"detail": err.Error(),
		})
	}

	// check it is for this upload
	if sig.Data.UploadID != trackId {
		return c.JSON(401, map[string]string{
			"error":  "signature contains incorrect track ID",
			"detail": fmt.Sprintf("url: %s, signature %s", trackId, sig.Data.UploadID),
		})
	}

	var cid string
	ss.crud.DB.Raw("SELECT cid FROM sound_recordings WHERE track_id = ?", trackId).Scan(&cid)
	if cid == "" {
		return c.JSON(404, "track not found")
	}

	var count int
	ss.crud.DB.Raw("SELECT COUNT(*) FROM management_keys WHERE track_id = ? AND pub_key = ?", trackId, base64.StdEncoding.EncodeToString(sig.SignerPubkey)).Scan(&count)
	if count == 0 {
		ss.logger.Debug("sig no match", zap.String("signed by", sig.SignerWallet))
		return c.JSON(401, map[string]string{
			"error":  "signer not authorized to access",
			"detail": "signed by: " + sig.SignerWallet,
		})
	}

	key := cidutil.ShardCID(cid)

	// if the client provided a filename, set it in the header to be auto-populated in download prompt
	filenameForDownload := c.QueryParam("filename")
	if filenameForDownload != "" {
		contentDisposition := mime.QEncoding.Encode("utf-8", filenameForDownload)
		c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, contentDisposition))
	}

	blob, err := ss.bucket.NewReader(ctx, key, nil)
	// If our bucket doesn't have the file, find a different node
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			// redirect to it
			host := ss.findNodeToServeBlob(ctx, cid)
			if host == "" {
				return c.String(404, "blob not found")
			}
			dest := ss.replaceHost(c, host)
			query := dest.Query()
			dest.RawQuery = query.Encode()
			return c.Redirect(302, dest.String())
		}
		return err
	}

	defer func() {
		if blob != nil {
			blob.Close()
		}
	}()

	// track metrics in separate threads
	go ss.logTrackListen(c)
	setTimingHeader(c)
	go ss.recordMetric(StreamTrack)

	// stream audio
	http.ServeContent(c.Response(), c.Request(), cid, blob.ModTime(), blob)
	return nil
}
