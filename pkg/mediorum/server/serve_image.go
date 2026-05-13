package server

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/erni27/imcache"
	"go.uber.org/zap"

	"github.com/labstack/echo/v4"
	"gocloud.dev/blob"
)

func (ss *MediorumServer) serveImage(c echo.Context) error {
	// images are small enough to always serve all at once (no 206 range responses)
	c.Request().Header.Del("Range")

	ctx := c.Request().Context()
	containerCID := c.Param("jobID")
	variant := c.Param("variant")
	skipCache, _ := strconv.ParseBool(c.QueryParam("skipCache"))
	isOriginalJpg := variant == "original.jpg"
	cacheKey := containerCID + variant

	serveSuccessWithBytes := func(blobData []byte, modTime time.Time) error {
		setTimingHeader(c)
		c.Response().Header().Set(echo.HeaderCacheControl, "public, max-age=2592000, immutable")
		http.ServeContent(c.Response(), c.Request(), cacheKey, modTime, bytes.NewReader(blobData))
		return nil
	}

	// helper function... only sets cache-control header on success
	serveSuccessWithReader := func(blob *blob.Reader) error {
		blobData, err := io.ReadAll(blob)
		if err != nil {
			return err
		}
		blob.Close()
		ss.imageCache.Set(cacheKey, blobData, imcache.WithNoExpiration())
		return serveSuccessWithBytes(blobData, blob.ModTime())
	}

	// use cache if possible
	if !skipCache {
		if blobData, ok := ss.imageCache.Get(cacheKey); ok && len(blobData) > 0 {
			c.Response().Header().Set("x-image-cache-hit", "true")
			return serveSuccessWithBytes(blobData, ss.StartedAt)
		}
	}

	// if the client provided a filename, set it in the header to be auto-populated in download prompt
	filenameForDownload := c.QueryParam("filename")
	if filenameForDownload != "" {
		contentDisposition := mime.QEncoding.Encode("utf-8", filenameForDownload)
		c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, contentDisposition))
	}

	// 1. resolve ulid to upload cid
	if cid, err := ss.getUploadOrigCID(containerCID); err == nil {
		c.Response().Header().Set("x-orig-cid", cid)
		containerCID = cid
	}

	// origImageCID is the identifier findAndPullBlob/replicateToMyBucket use
	// when fetching the original. For non-legacy CIDs it equals containerCID;
	// for legacy CIDs it's "<cid>/original.jpg" — a *different* rendezvous-rank
	// input. Route reads of the original by origImageCID so they land in the
	// same bucket the pull wrote to.
	//
	// Variants (resized derivatives) are pure on-demand cache — regenerated
	// from the original by Resized() below — and always live in the primary
	// bucket. Putting regeneratable cache in cold storage would force
	// retrieval fees on every image request that hit a node holding the
	// original in archive.
	origImageCID := containerCID
	if cidutil.IsLegacyCID(origImageCID) {
		origImageCID += "/original.jpg"
	}

	serveSuccess := func(blobPath string) error {
		// Use readBlob so original.jpg requests still resolve when the
		// original lives in archive — variantStoragePath for original.jpg
		// is the actual original's storage key, and only resized variants
		// are guaranteed to live in primary.
		if blob, _, err := ss.readBlob(ctx, blobPath); err == nil {
			return serveSuccessWithReader(blob)
		} else {
			return err
		}
	}

	// 2. serve variant
	// parse 150x150 dimensions
	// while still allowing original.jpg

	var variantStoragePath string
	w, h, err := parseVariantSize(variant)
	if err == nil {
		variantStoragePath = cidutil.ImageVariantPath(containerCID, variant)
	} else if isOriginalJpg {
		if cidutil.IsLegacyCID(containerCID) {
			variantStoragePath = containerCID + "/original.jpg"
		} else {
			variantStoragePath = cidutil.ShardCID(containerCID)
		}
	} else {
		return c.String(400, err.Error())
	}

	c.Response().Header().Set("x-variant-storage-path", variantStoragePath)

	// we already have the resized version (variants always live in primary)
	if !skipCache {
		if blob, err := ss.bucket.NewReader(ctx, variantStoragePath, nil); err == nil {
			return serveSuccessWithReader(blob)
		}
	}

	// open the orig for resizing — readBlob falls back to archive when the
	// original lives there (e.g. on a StoreAll+archive node).
	origReader, _, err := ss.readBlob(ctx, cidutil.ShardCID(origImageCID))

	// if we don't have orig, fetch from network
	if err != nil {
		startFetch := time.Now()
		host, pullErr := ss.findAndPullBlob(ctx, origImageCID, nil)
		if pullErr != nil {
			// Pull failed - check if it's due to disk space
			if !ss.diskHasSpaceForCID(origImageCID, nil) {
				// Disk is full, proxy the request instead of erroring
				// Redirect to a node that can serve this variant
				redirectHost := ss.findNodeToServeBlob(ctx, origImageCID)
				if redirectHost == "" {
					return c.String(404, "blob not found")
				}
				dest := ss.replaceHost(c, redirectHost)
				var query url.Values = dest.Query()
				query.Add("allow_unhealthy", "true")
				dest.RawQuery = query.Encode()
				return c.Redirect(302, dest.String())
			}
			// Pull failed for other reasons, redirect to a node that has it
			redirectHost := ss.findNodeToServeBlob(ctx, origImageCID)
			if redirectHost == "" {
				return c.String(404, pullErr.Error())
			}
			dest := ss.replaceHost(c, redirectHost)
			var query url.Values = dest.Query()
			query.Add("allow_unhealthy", "true")
			dest.RawQuery = query.Encode()
			return c.Redirect(302, dest.String())
		}

		c.Response().Header().Set("x-fetch-host", host)
		c.Response().Header().Set("x-fetch-ok", fmt.Sprintf("%.2fs", time.Since(startFetch).Seconds()))

		origReader, _, err = ss.readBlob(ctx, cidutil.ShardCID(origImageCID))
		if err != nil {
			return err
		}
	}

	// do resize if not original.jpg. Variants always go to primary — see comment above.
	if !isOriginalJpg {
		resizeStart := time.Now()
		resized, _, _ := Resized(".jpg", origReader, w, h, "fill")
		// Variants are best-effort cache. If the write to primary fails (full
		// disk, transient backend error), log and skip the cache step rather
		// than nil-deref on io.Copy or fail the user's request — the resized
		// bytes are served from `serveSuccess` below either way (next request
		// regenerates the variant).
		if vw, vwErr := ss.bucket.NewWriter(ctx, variantStoragePath, nil); vwErr == nil {
			if _, copyErr := io.Copy(vw, resized); copyErr != nil {
				ss.logger.Warn("variant cache write copy failed", zap.String("path", variantStoragePath), zap.Error(copyErr))
			}
			if closeErr := vw.Close(); closeErr != nil {
				ss.logger.Warn("variant cache write close failed", zap.String("path", variantStoragePath), zap.Error(closeErr))
			}
		} else {
			ss.logger.Warn("variant cache writer open failed", zap.String("path", variantStoragePath), zap.Error(vwErr))
		}
		c.Response().Header().Set("x-resize-ok", fmt.Sprintf("%.2fs", time.Since(resizeStart).Seconds()))
	}
	origReader.Close()

	// ... serve it
	return serveSuccess(variantStoragePath)
}
