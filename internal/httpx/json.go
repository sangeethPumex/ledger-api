package httpx

import (
	"encoding/json"
	"net/http"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func Err(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// Decode reads a small JSON body; on failure it has already replied 400.
func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		Err(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// PathID parses the {id} path value; on failure it has already replied 400.
func PathID(w http.ResponseWriter, r *http.Request) (bson.ObjectID, bool) {
	id, err := bson.ObjectIDFromHex(r.PathValue("id"))
	if err != nil {
		Err(w, http.StatusBadRequest, "invalid id")
	}
	return id, err == nil
}
