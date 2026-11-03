package vehicles

import (
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

const maxFile = 15 << 20

// fileTypes are what the bucket accepts; a photo must be one of the images.
var fileTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "application/pdf": true}

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	h := func(f http.HandlerFunc) http.Handler { return auth(f) }
	mux.Handle("GET /vehicles", h(c.list))
	mux.Handle("GET /vehicles/suggestions", h(c.suggestions))
	mux.Handle("POST /vehicles", h(c.create))
	mux.Handle("GET /vehicles/{id}", h(c.get))
	mux.Handle("PUT /vehicles/{id}", h(c.update))
	mux.Handle("DELETE /vehicles/{id}", h(c.delete))
	mux.Handle("POST /vehicles/{id}/logs", h(c.addLog))
	mux.Handle("PUT /vehicles/{id}/logs/{log}", h(c.updateLog))
	mux.Handle("DELETE /vehicles/{id}/logs/{log}", h(c.deleteLog))
	mux.Handle("POST /vehicles/{id}/files", h(c.upload))
	mux.Handle("GET /vehicles/{id}/files/{file}", h(c.file))
	mux.Handle("DELETE /vehicles/{id}/files/{file}", h(c.deleteFile))
}

// done replies 204 for success, 404 for something missing, 503 with no bucket, and 500 otherwise.
func done(w http.ResponseWriter, err error, what string) {
	switch {
	case err == nil:
		w.WriteHeader(204)
	case errors.Is(err, mongo.ErrNoDocuments):
		httpx.Err(w, 404, "not found")
	case errors.Is(err, ErrNoStore):
		httpx.Err(w, 503, err.Error())
	default:
		log.Printf("vehicles: %s: %v", what, err)
		httpx.Err(w, 500, "could not "+what)
	}
}

func (c *Controller) list(w http.ResponseWriter, r *http.Request) {
	out, err := c.svc.List(r.Context(), time.Now())
	if err != nil {
		done(w, err, "list vehicles")
		return
	}
	httpx.WriteJSON(w, 200, out)
}

func (c *Controller) suggestions(w http.ResponseWriter, r *http.Request) {
	out, err := c.svc.Suggestions(r.Context(), time.Now())
	if err != nil {
		done(w, err, "look for vehicle spending")
		return
	}
	httpx.WriteJSON(w, 200, out)
}

func (c *Controller) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	d, err := c.svc.Get(r.Context(), id, time.Now())
	if err != nil {
		done(w, err, "load vehicle")
		return
	}
	httpx.WriteJSON(w, 200, d)
}

func (c *Controller) create(w http.ResponseWriter, r *http.Request) {
	var in VehicleInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, msg := in.Parse()
	if msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	v, err := c.svc.Create(r.Context(), v)
	if err != nil {
		done(w, err, "add vehicle")
		return
	}
	httpx.WriteJSON(w, 201, v)
}

func (c *Controller) update(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in VehicleInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, msg := in.Parse()
	if msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	done(w, c.svc.Update(r.Context(), id, v), "update vehicle")
}

func (c *Controller) delete(w http.ResponseWriter, r *http.Request) {
	if id, ok := httpx.PathID(w, r); ok {
		done(w, c.svc.Delete(r.Context(), id), "delete vehicle")
	}
}

func logID(w http.ResponseWriter, r *http.Request) (bson.ObjectID, bson.ObjectID, bool) {
	vid, ok := httpx.PathID(w, r)
	if !ok {
		return vid, vid, false
	}
	id, err := bson.ObjectIDFromHex(r.PathValue("log"))
	if err != nil {
		httpx.Err(w, 400, "invalid log id")
	}
	return vid, id, err == nil
}

func decodeLog(w http.ResponseWriter, r *http.Request) (Log, bool) {
	var in LogInput
	if !httpx.Decode(w, r, &in) {
		return Log{}, false
	}
	l, msg := in.Parse()
	if msg != "" {
		httpx.Err(w, 400, msg)
	}
	return l, msg == ""
}

func (c *Controller) addLog(w http.ResponseWriter, r *http.Request) {
	vid, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	if l, ok := decodeLog(w, r); ok {
		done(w, c.svc.AddLog(r.Context(), vid, l), "save entry")
	}
}

func (c *Controller) updateLog(w http.ResponseWriter, r *http.Request) {
	vid, id, ok := logID(w, r)
	if !ok {
		return
	}
	if l, ok := decodeLog(w, r); ok {
		done(w, c.svc.UpdateLog(r.Context(), vid, id, l), "update entry")
	}
}

func (c *Controller) deleteLog(w http.ResponseWriter, r *http.Request) {
	if vid, id, ok := logID(w, r); ok {
		done(w, c.svc.DeleteLog(r.Context(), vid, id), "delete entry")
	}
}

// upload takes the file as the raw body: ?kind=photo|insurance|rc|puc|service|receipt|other&name=policy.pdf, with its Content-Type.
func (c *Controller) upload(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	kind, name := r.URL.Query().Get("kind"), strings.TrimSpace(r.URL.Query().Get("name"))
	ctype := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	switch {
	case kind != "photo" && !docKinds[kind]:
		httpx.Err(w, 400, "unknown kind of file")
		return
	case !fileTypes[ctype] || kind == "photo" && !strings.HasPrefix(ctype, "image/"):
		httpx.Err(w, 400, "use a PNG, JPEG or WebP image, or a PDF")
		return
	case name == "" || len(name) > 120:
		name = kind
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFile))
	if err != nil {
		httpx.Err(w, 413, "files must be under 15 MB")
		return
	}
	if len(data) == 0 {
		httpx.Err(w, 400, "the file is empty")
		return
	}
	d, err := c.svc.SaveFile(r.Context(), id, kind, name, ctype, data)
	if err != nil {
		done(w, err, "save file")
		return
	}
	httpx.WriteJSON(w, 201, d)
}

func (c *Controller) file(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	body, ctype, err := c.svc.File(r.Context(), id, r.PathValue("file"))
	if err != nil {
		done(w, err, "open file")
		return
	}
	defer body.Close()
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	// Keys never change content (a new upload gets a new key), so the browser may keep it.
	w.Header().Set("Cache-Control", "private, max-age=604800")
	if _, err := io.Copy(w, body); err != nil {
		log.Printf("vehicles: send file: %v", err)
	}
}

func (c *Controller) deleteFile(w http.ResponseWriter, r *http.Request) {
	if id, ok := httpx.PathID(w, r); ok {
		done(w, c.svc.DeleteFile(r.Context(), id, r.PathValue("file")), "delete file")
	}
}
