package agentserver

import (
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var markRoute = regexp.MustCompile(`^/marks/[GA]([0-9]{1,10})_[0-9]{1,10}_[0-9]{1,10}\.crb$`)

// Public native marks are selected by the server prefix, never a client URL.
// Keep the private control origin and credentials out of the response.
func (server *Server) handleMark(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	match := markRoute.FindStringSubmatch(r.URL.Path)
	if match == nil || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	prefix, err := strconv.ParseUint(match[1], 10, 16)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target := ""
	for _, definition := range server.currentCatalog().Definitions() {
		if definition.Enabled && uint64(definition.NativeServerID) == prefix {
			target = strings.TrimSuffix(definition.ControlURL, "/") + r.URL.Path
			break
		}
	}
	if target == "" {
		http.NotFound(w, r)
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		http.Error(w, "mark unavailable", http.StatusBadGateway)
		return
	}
	for _, key := range []string{"If-None-Match", "If-Modified-Since"} {
		if value := r.Header.Get(key); value != "" {
			request.Header.Set(key, value)
		}
	}
	response, err := server.client.Do(request)
	if err != nil {
		http.Error(w, "mark unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		http.NotFound(w, r)
		return
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotModified {
		http.Error(w, "mark unavailable", http.StatusBadGateway)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 257))
	if err != nil || response.StatusCode == http.StatusOK && len(body) != 256 {
		http.Error(w, "invalid mark", http.StatusBadGateway)
		return
	}
	for _, key := range []string{"ETag", "Last-Modified", "Cache-Control"} {
		if value := response.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.StatusCode)
	if response.StatusCode == http.StatusOK {
		_, _ = w.Write(body)
	}
}
