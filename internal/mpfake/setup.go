package mpfake

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
)

const fakeUserID = "1234567890"

func (s *Server) me(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"id": json.Number(fakeUserID), "site_id": "MLB", "nickname": "TESTUSER"})
}

func (s *Server) searchStores(w http.ResponseWriter, r *http.Request) {
	externalID := r.URL.Query().Get("external_id")
	s.mu.Lock()
	defer s.mu.Unlock()
	results := []mercadopago.Store{}
	for _, st := range s.stores {
		if externalID == "" || st.ExternalID == externalID {
			results = append(results, st)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"paging": map[string]int{"total": len(results)}, "results": results})
}

func (s *Server) createStore(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("userID") != fakeUserID {
		writeError(w, http.StatusForbidden, "forbidden", "user mismatch")
		return
	}
	var st mercadopago.Store
	if err := json.NewDecoder(r.Body).Decode(&st); err != nil || st.Name == "" || st.ExternalID == "" || st.Location == nil {
		writeError(w, http.StatusBadRequest, "invalid_store", "name, external_id and location are required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.stores {
		if existing.ExternalID == st.ExternalID {
			writeError(w, http.StatusConflict, "duplicated_external_id", "store external_id already exists")
			return
		}
	}
	st.ID = s.nextID()
	s.stores = append(s.stores, st)
	writeJSON(w, http.StatusCreated, st)
}

func (s *Server) searchPOS(w http.ResponseWriter, r *http.Request) {
	externalID := r.URL.Query().Get("external_id")
	s.mu.Lock()
	defer s.mu.Unlock()
	results := []mercadopago.POS{}
	for _, p := range s.pos {
		if externalID == "" || p.ExternalID == externalID {
			results = append(results, p)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"paging": map[string]int{"total": len(results)}, "results": results})
}

func (s *Server) createPOS(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("X-Idempotency-Key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "X-Idempotency-Key is required")
		return
	}
	var p mercadopago.POS
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.Name == "" || p.ExternalID == "" || p.StoreID == "" {
		writeError(w, http.StatusBadRequest, "invalid_pos", "name, store_id and external_id are required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.posByKey[key]; ok {
		writeJSON(w, http.StatusCreated, previous)
		return
	}
	if !s.hasStore(p.StoreID) {
		writeError(w, http.StatusBadRequest, "store_not_found", "store not found")
		return
	}
	p.ID = s.nextID()
	p.Status = "active"
	s.pos = append(s.pos, p)
	s.posByKey[key] = p
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) hasStore(id string) bool {
	for _, st := range s.stores {
		if st.ID.String() == id {
			return true
		}
	}
	return false
}

func (s *Server) nextID() json.Number {
	s.nextIDNum++
	return json.Number(strconv.FormatInt(s.nextIDNum, 10))
}
