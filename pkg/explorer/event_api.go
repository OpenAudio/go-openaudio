package explorer

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
)

// eventResponse is the JSON shape returned by the event API endpoints.
type eventResponse struct {
	EventID     int32         `json:"event_id"`
	EventType   string        `json:"event_type"`
	UserID      int32         `json:"user_id"`
	EntityType  string        `json:"entity_type"`
	EntityID    *int32        `json:"entity_id"`
	EndDate     string        `json:"end_date"`
	EventData   interface{}   `json:"event_data"`
	CreatedAt   string        `json:"created_at"`
	Permalink   string        `json:"permalink"`
}

// GetEvent handles GET /api/v1/events/:id
func (ex *Explorer) GetEvent(c echo.Context) error {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid event id"})
	}
	queries := ex.etl.GetDB()
	if queries == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "etl not ready"})
	}
	ev, err := queries.GetEventByID(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "event not found"})
		}
		ex.logger.Error("GetEvent DB error")
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, toEventResponse(ev))
}

// GetEventBySlug handles GET /api/v1/events/:handle/:slug
func (ex *Explorer) GetEventBySlug(c echo.Context) error {
	handle := c.Param("handle")
	slug := c.Param("slug")
	if handle == "" || slug == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "handle and slug are required"})
	}
	queries := ex.etl.GetDB()
	if queries == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "etl not ready"})
	}
	ev, err := queries.GetEventBySlug(c.Request().Context(), handle, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "event not found"})
		}
		ex.logger.Error("GetEventBySlug DB error")
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, toEventResponse(ev))
}

// ListEvents handles GET /api/v1/events
func (ex *Explorer) ListEvents(c echo.Context) error {
	limit := int32(50)
	offset := int32(0)
	if v := c.QueryParam("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}
	if v := c.QueryParam("offset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n >= 0 {
			offset = int32(n)
		}
	}
	queries := ex.etl.GetDB()
	if queries == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "etl not ready"})
	}
	evs, err := queries.ListEvents(c.Request().Context(), db.ListEventsParams{Limit: limit, Offset: offset})
	if err != nil {
		ex.logger.Error("ListEvents DB error")
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	resp := make([]eventResponse, 0, len(evs))
	for _, ev := range evs {
		resp = append(resp, toEventResponse(ev))
	}
	return c.JSON(http.StatusOK, resp)
}

// toEventResponse converts a DB EventWithPermalink into the API response shape.
func toEventResponse(ev db.EventWithPermalink) eventResponse {
	r := eventResponse{
		EventID:   ev.EventID,
		EventType: ev.EventType,
		UserID:    ev.UserID,
		Permalink: ev.Permalink,
	}
	if ev.EntityType.Valid {
		r.EntityType = ev.EntityType.String
	}
	if ev.EntityID.Valid {
		v := ev.EntityID.Int32
		r.EntityID = &v
	}
	if ev.EndDate.Valid {
		r.EndDate = ev.EndDate.Time.UTC().Format("2006-01-02T15:04:05Z")
	}
	if ev.CreatedAt.Valid {
		r.CreatedAt = ev.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z")
	}
	if len(ev.EventData) > 0 {
		var obj interface{}
		if jsonErr := json.Unmarshal(ev.EventData, &obj); jsonErr == nil {
			r.EventData = obj
		} else {
			r.EventData = string(ev.EventData)
		}
	}
	return r
}