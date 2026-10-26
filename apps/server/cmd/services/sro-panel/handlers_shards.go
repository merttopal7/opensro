/*
===========================================================================

handlers_shards.go - Shard provisioning, Check mode, MaxLevel, GMs, and Bans

===========================================================================
*/
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"opensro.online/server/internal/cluster/shard"
	"opensro.online/server/internal/data/store"
	"opensro.online/server/internal/gamedata"
	"opensro.online/server/internal/security/auth"
)

var validShardIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,26}[a-z0-9]$`)

/*
================
syncCatalogToDatabase
================
*/
func (s *panelServer) syncCatalogToDatabase() {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()

	cat, err := shard.Load(s.catalogPath)
	if err != nil {
		return
	}
	existingRows, _ := s.accounts.ShardDefinitions()
	existingByID := make(map[string]auth.ShardRow, len(existingRows))
	for _, row := range existingRows {
		existingByID[row.ID] = row
	}
	for _, def := range cat.Definitions() {
		prev, ok := existingByID[def.ID]
		owner := def.OwnerAccountID
		checkMode := def.CheckMode
		maxLevel := def.MaxLevel
		if ok {
			if owner == "" {
				owner = prev.OwnerAccountID
			}
			checkMode = prev.CheckMode
			if prev.MaxLevel >= 1 && prev.MaxLevel <= 140 {
				maxLevel = prev.MaxLevel
			}
		}
		if maxLevel < 1 || maxLevel > 140 {
			maxLevel = 90
		}
		_ = s.accounts.UpsertShard(context.Background(), auth.ShardRow{
			ID:                 def.ID,
			Name:               def.Name,
			NativeServerID:     def.NativeServerID,
			NativeFarmID:       uint16(def.NativeFarmID),
			Capacity:           def.Capacity,
			Default:            def.Default,
			Test:               def.Test,
			Enabled:            def.Enabled,
			ControlURL:         def.ControlURL,
			TransportURL:       def.TransportURL,
			PublicTransportURL: def.PublicTransportURL,
			OwnerAccountID:     owner,
			CheckMode:          checkMode,
			MaxLevel:           maxLevel,
		})
	}
}

/*
================
saveDatabaseToCatalogFile
================
*/
func (s *panelServer) saveDatabaseToCatalogFile() error {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()

	rows, err := s.accounts.ShardDefinitions()
	if err != nil {
		return err
	}
	type fileShard struct {
		ID                 string `json:"id"`
		Name               string `json:"name"`
		NativeServerID     uint16 `json:"nativeServerId"`
		NativeFarmID       uint8  `json:"nativeFarmId"`
		Capacity           int    `json:"capacity"`
		Default            bool   `json:"default,omitempty"`
		Test               bool   `json:"test,omitempty"`
		Enabled            bool   `json:"enabled"`
		ControlURL         string `json:"controlUrl"`
		TransportURL       string `json:"transportUrl"`
		PublicTransportURL string `json:"publicTransportUrl,omitempty"`
		OwnerAccountID     string `json:"ownerAccountId,omitempty"`
		CheckMode          bool   `json:"checkMode,omitempty"`
		MaxLevel           int    `json:"maxLevel,omitempty"`
	}
	payload := struct {
		Shards []fileShard `json:"shards"`
	}{
		Shards: make([]fileShard, 0, len(rows)),
	}
	for _, r := range rows {
		payload.Shards = append(payload.Shards, fileShard{
			ID:                 r.ID,
			Name:               r.Name,
			NativeServerID:     r.NativeServerID,
			NativeFarmID:       uint8(r.NativeFarmID),
			Capacity:           r.Capacity,
			Default:            r.Default,
			Test:               r.Test,
			Enabled:            r.Enabled,
			ControlURL:         r.ControlURL,
			TransportURL:       r.TransportURL,
			PublicTransportURL: r.PublicTransportURL,
			OwnerAccountID:     r.OwnerAccountID,
			CheckMode:          r.CheckMode,
			MaxLevel:           r.MaxLevel,
		})
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.catalogPath, append(encoded, '\n'), 0o644)
}

/*
================
canManageShard
================
*/
func (s *panelServer) canManageShard(accountID string, row auth.ShardRow) bool {
	if accountID == "" {
		return false
	}
	if s.accounts.IsGM(accountID) {
		return true
	}
	if row.OwnerAccountID != "" && strings.EqualFold(row.OwnerAccountID, accountID) {
		return true
	}
	return s.accounts.IsGMOnShard(accountID, row.ID)
}

/*
================
isShardOwnerOrAdmin
================
*/
func (s *panelServer) isShardOwnerOrAdmin(accountID string, row auth.ShardRow) bool {
	if accountID == "" {
		return false
	}
	if s.accounts.IsGM(accountID) {
		return true
	}
	return row.OwnerAccountID != "" && strings.EqualFold(row.OwnerAccountID, accountID)
}

/*
================
findShardRow
================
*/
func (s *panelServer) findShardRow(shardID string) (auth.ShardRow, bool) {
	rows, err := s.accounts.ShardDefinitions()
	if err != nil {
		return auth.ShardRow{}, false
	}
	for _, r := range rows {
		if r.ID == shardID {
			return r, true
		}
	}
	return auth.ShardRow{}, false
}

/*
================
shardView
================
*/
type shardView struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	NativeServerID uint16             `json:"nativeServerId"`
	Capacity       int                `json:"capacity"`
	Enabled        bool               `json:"enabled"`
	Operating      bool               `json:"operating"`
	ProcessRunning bool               `json:"processRunning"`
	Healthy        bool               `json:"healthy"`
	Deploying      bool               `json:"deploying"`
	BackendExists  bool               `json:"backendExists"`
	BackendStatus  string             `json:"backendStatus"`
	BackendWarning string             `json:"backendWarning,omitempty"`
	CheckMode      bool               `json:"checkMode"`
	MaxLevel       int                `json:"maxLevel"`
	OnlinePlayers  int                `json:"onlinePlayers"`
	OwnerAccountID string             `json:"ownerAccountId"`
	OwnerUserID    string             `json:"ownerUserId"`
	IsOwner        bool               `json:"isOwner"`
	CanManage      bool               `json:"canManage"`
	CanClaim       bool               `json:"canClaim"`
	GMs            []string           `json:"gms,omitempty"`
	GMUsers        []string           `json:"gmUsers"`
	Bans           []auth.ShardBanRow `json:"bans,omitempty"`
	BannedUsers    []string           `json:"bannedUsers"`
}

/*
================
nomadBaseURL
================
*/
func (s *panelServer) nomadBaseURL() string {
	if addr := strings.TrimRight(strings.TrimSpace(os.Getenv("NOMAD_ADDR")), "/"); addr != "" {
		return addr
	}
	offset := 0
	if raw := strings.TrimSpace(os.Getenv("SRO_DEV_PORT_OFFSET")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	return fmt.Sprintf("http://127.0.0.1:%d", 4646+offset)
}

/*
================
probeShardBackgroundStatus

Inspects whether the shard actually exists in the background (authority state.db
and Nomad job sro-gameworld-<id>) when /transport/readyz is not yet Healthy.
Returns (backendStatus, backendExists, backendWarning):
  - "healthy"   : GameWorld is running and /transport/readyz is HTTP 200
  - "deploying" : Nomad deployment or sro-nomad deploy is actively running
  - "disabled"  : Written in catalog/DB with enabled=false; not in Nomad
  - "missing"   : Written in catalog/DB, but authority DB or Nomad job does not exist
  - "failed"    : Nomad job exists, but its deployment failed or has 0 allocations

================
*/
func (s *panelServer) probeShardBackgroundStatus(row auth.ShardRow, processRunning bool) (string, bool, string) {
	if processRunning {
		return "healthy", true, ""
	}
	if !row.Enabled {
		return "disabled", false, fmt.Sprintf(
			"'%s' sunucusu şu anda devre dışı durumunda ve aktif bir sunucu örneği çalıştırılmıyor.",
			row.Name,
		)
	}
	dbPath := filepath.Join(s.moduleRoot, ".state", "shards", row.ID, "authority", store.DBFileName)
	if _, err := os.Stat(dbPath); err != nil {
		return "missing", false, fmt.Sprintf(
			"'%s' sunucusu henüz kurulmamış ve çalışan bir sunucu örneği bulunmuyor.",
			row.Name,
		)
	}
	if s.deployRunning.Load() > 0 {
		return "deploying", true, ""
	}

	nomadBase := s.nomadBaseURL()
	jobID := "sro-gameworld-" + row.ID
	jobReq, err := http.NewRequest(http.MethodGet, nomadBase+"/v1/job/"+jobID, nil)
	if err != nil {
		return "missing", false, "Sunucu durum sorgusu oluşturulamadı."
	}
	jobResp, err := s.httpClient.Do(jobReq)
	if err != nil {
		return "missing", false, fmt.Sprintf(
			"'%s' sunucu örneği şu anda çalışmıyor.",
			row.Name,
		)
	}
	jobBody, _ := io.ReadAll(io.LimitReader(jobResp.Body, 16384))
	_ = jobResp.Body.Close()

	if jobResp.StatusCode == http.StatusNotFound || strings.Contains(strings.ToLower(string(jobBody)), "job not found") {
		return "missing", false, fmt.Sprintf(
			"'%s' için çalışan aktif bir oyun sunucusu bulunamadı.",
			row.Name,
		)
	}
	if jobResp.StatusCode != http.StatusOK {
		return "missing", false, fmt.Sprintf(
			"'%s' sunucu durumu doğrulanamadı.",
			row.Name,
		)
	}

	var jobInfo struct {
		Status string `json:"Status"`
		Stop   bool   `json:"Stop"`
	}
	_ = json.Unmarshal(jobBody, &jobInfo)
	if jobInfo.Stop || strings.EqualFold(jobInfo.Status, "dead") {
		return "missing", false, fmt.Sprintf(
			"'%s' sunucusu şu anda durdurulmuş durumda.",
			row.Name,
		)
	}

	depReq, err := http.NewRequest(http.MethodGet, nomadBase+"/v1/job/"+jobID+"/deployment", nil)
	if err == nil {
		if depResp, depErr := s.httpClient.Do(depReq); depErr == nil {
			depBody, _ := io.ReadAll(io.LimitReader(depResp.Body, 16384))
			_ = depResp.Body.Close()
			if depResp.StatusCode == http.StatusOK && len(depBody) > 0 && string(bytes.TrimSpace(depBody)) != "null" {
				var depInfo struct {
					Status            string `json:"Status"`
					StatusDescription string `json:"StatusDescription"`
				}
				if json.Unmarshal(depBody, &depInfo) == nil {
					switch strings.ToLower(depInfo.Status) {
					case "failed", "cancelled":
						return "failed", false, fmt.Sprintf(
							"'%s' sunucusu başlatılırken bir hata oluştu. Lütfen sunucuyu yeniden başlatın.",
							row.Name,
						)
					case "running", "initializing", "pending":
						return "deploying", true, ""
					}
				}
			}
		}
	}

	return "deploying", true, ""
}

/*
================
buildShardViews
================
*/
func (s *panelServer) buildShardViews(accountID string) []shardView {
	rows, err := s.accounts.ShardDefinitions()
	if err != nil {
		return []shardView{}
	}
	out := make([]shardView, 0, len(rows))
	for _, row := range rows {
		canManage := s.canManageShard(accountID, row)
		isOwner := s.isShardOwnerOrAdmin(accountID, row)
		maxLv := row.MaxLevel
		if maxLv < 1 || maxLv > 140 {
			maxLv = 110
		}
		processRunning, liveOnline := s.probeShardLiveStatus(row)
		onlineCount := row.OnlinePlayers
		if processRunning && liveOnline >= 0 {
			onlineCount = liveOnline
		}
		operating := processRunning && !row.CheckMode
		backendStatus, backendExists, backendWarning := s.probeShardBackgroundStatus(row, processRunning)
		view := shardView{
			ID:             row.ID,
			Name:           row.Name,
			NativeServerID: row.NativeServerID,
			Capacity:       row.Capacity,
			Enabled:        row.Enabled,
			Operating:      operating,
			ProcessRunning: processRunning,
			Healthy:        processRunning,
			Deploying:      backendStatus == "deploying",
			BackendExists:  backendExists,
			BackendStatus:  backendStatus,
			BackendWarning: backendWarning,
			CheckMode:      row.CheckMode,
			MaxLevel:       maxLv,
			OnlinePlayers:  onlineCount,
			OwnerAccountID: row.OwnerAccountID,
			OwnerUserID:    row.OwnerAccountID,
			IsOwner:        isOwner,
			CanManage:      canManage,
			CanClaim:       row.OwnerAccountID == "",
			GMUsers:        []string{},
			BannedUsers:    []string{},
		}
		if canManage {
			gms, _ := s.accounts.ListShardGMs(row.ID)
			bans, _ := s.accounts.ListShardBans(row.ID)
			if gms != nil {
				view.GMs = gms
				view.GMUsers = gms
			}
			view.Bans = bans
			for _, b := range bans {
				view.BannedUsers = append(view.BannedUsers, b.AccountID)
			}
		}
		out = append(out, view)
	}
	return out
}

/*
================
handleShards
================
*/
func (s *panelServer) handleShards(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authenticatedAccount(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "Oturum açmanız gerekiyor.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"shards": s.buildShardViews(accountID),
		})

	case http.MethodPost:
		s.handleCreateShard(w, r, accountID)

	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

/*
================
handleCreateShard
================
*/
func (s *panelServer) handleCreateShard(w http.ResponseWriter, r *http.Request, accountID string) {
	var req struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Capacity  int    `json:"capacity"`
		MaxLevel  int    `json:"maxLevel"`
		CheckMode bool   `json:"checkMode"`
		Operating *bool  `json:"operating"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Geçersiz istek formatı.")
		return
	}
	checkMode := req.CheckMode
	if req.Operating != nil {
		checkMode = !*req.Operating
	}
	shardID := strings.ToLower(strings.TrimSpace(req.ID))
	if !validShardIDPattern.MatchString(shardID) {
		writeError(w, http.StatusBadRequest, "Shard kodu 3-28 karakter olmalı, yalnızca küçük harf, rakam ve tire (-) içermelidir (örn: olympus).")
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) < 2 || len(name) > 32 {
		writeError(w, http.StatusBadRequest, "Shard görünen adı 2-32 karakter arasında olmalıdır.")
		return
	}
	capacity := req.Capacity
	if capacity < 1 || capacity > 3500 {
		capacity = 500
	}
	maxLevel := req.MaxLevel
	if maxLevel < 1 || maxLevel > 140 {
		maxLevel = 110
	}

	rows, err := s.accounts.ShardDefinitions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Mevcut shard listesi okunamadı.")
		return
	}
	maxServerID := uint16(2)
	usedPorts := make(map[int]bool)
	for _, row := range rows {
		if strings.EqualFold(row.ID, shardID) {
			writeError(w, http.StatusConflict, "Bu shard kodu zaten kullanılıyor.")
			return
		}
		if strings.EqualFold(row.Name, name) {
			writeError(w, http.StatusConflict, "Bu shard adı zaten kullanılıyor.")
			return
		}
		if row.NativeServerID > maxServerID {
			maxServerID = row.NativeServerID
		}
		for _, rawURL := range []string{row.TransportURL, row.ControlURL} {
			var p int
			if idx := strings.LastIndex(rawURL, ":"); idx != -1 {
				_, _ = fmt.Sscanf(rawURL[idx+1:], "%d", &p)
				if p > 0 {
					usedPorts[p] = true
				}
			}
		}
	}
	transportPort := 8800
	for usedPorts[transportPort] || usedPorts[transportPort+1] {
		transportPort += 2
	}
	controlPort := transportPort + 1
	nativeServerID := maxServerID + 1

	authorityDir := filepath.Join(s.moduleRoot, ".state", "shards", shardID, "authority")
	if err := store.Initialize(authorityDir); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Shard veritabanı ilklendirilemedi: %v", err))
		return
	}
	_ = os.Remove(filepath.Join(authorityDir, store.LockFileName))
	if _, err := ensureShardOperatorToken(authorityDir); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Operator anahtarı oluşturulamadı: %v", err))
		return
	}

	newRow := auth.ShardRow{
		ID:                 shardID,
		Name:               name,
		NativeServerID:     nativeServerID,
		NativeFarmID:       0,
		Capacity:           capacity,
		Default:            false,
		Test:               false,
		Enabled:            true,
		ControlURL:         fmt.Sprintf("http://127.0.0.1:%d", controlPort),
		TransportURL:       fmt.Sprintf("http://127.0.0.1:%d", transportPort),
		PublicTransportURL: "/shards/" + shardID,
		OwnerAccountID:     accountID,
		CheckMode:          checkMode,
		MaxLevel:           maxLevel,
	}
	if err := s.accounts.UpsertShard(r.Context(), newRow); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Shard kaydedilemedi: %v", err))
		return
	}
	_ = s.accounts.GrantShardGM(r.Context(), accountID, shardID)
	if err := s.saveDatabaseToCatalogFile(); err != nil {
		writeError(w, http.StatusInternalServerError, "Sunucu yapılandırması kaydedilemedi.")
		return
	}

	go s.triggerNomadDeploy()

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"shardId": shardID,
		"message": "Shard oluşturuldu ve sunucu başlatılıyor (yaklaşık 15-25 sn içinde hazır olur).",
	})
}

/*
================
handleShardSubroutes
================
*/
func (s *panelServer) handleShardSubroutes(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authenticatedAccount(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "Oturum açmanız gerekiyor.")
		return
	}
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/shards/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "Geçersiz uç nokta.")
		return
	}
	shardID := parts[0]
	action := parts[1]

	row, exists := s.findShardRow(shardID)
	if !exists {
		writeError(w, http.StatusNotFound, "Shard bulunamadı.")
		return
	}

	if action == "claim" && r.Method == http.MethodPost {
		if row.OwnerAccountID != "" {
			writeError(w, http.StatusConflict, "Bu shard'ın zaten bir sahibi var.")
			return
		}
		row.OwnerAccountID = accountID
		if err := s.accounts.UpsertShard(r.Context(), row); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = s.accounts.GrantShardGM(r.Context(), accountID, shardID)
		_ = s.saveDatabaseToCatalogFile()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	if !s.canManageShard(accountID, row) {
		writeError(w, http.StatusForbidden, "Bu shard üzerinde yönetim yetkiniz yok.")
		return
	}

	if action == "redeploy" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		authorityDir := filepath.Join(s.moduleRoot, ".state", "shards", row.ID, "authority")
		dbPath := filepath.Join(authorityDir, store.DBFileName)
		if _, statErr := os.Stat(dbPath); statErr != nil {
			if initErr := store.Initialize(authorityDir); initErr != nil {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("Shard veritabanı ilklendirilemedi: %v", initErr))
				return
			}
		}
		if healthy, _ := s.probeShardLiveStatus(row); !healthy {
			_ = os.Remove(filepath.Join(authorityDir, store.LockFileName))
		}
		if _, tokErr := ensureShardOperatorToken(authorityDir); tokErr != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("Operator anahtarı oluşturulamadı: %v", tokErr))
			return
		}
		if !row.Enabled {
			row.Enabled = true
			if upErr := s.accounts.UpsertShard(r.Context(), row); upErr != nil {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("Shard etkinleştirilemedi: %v", upErr))
				return
			}
			_ = s.saveDatabaseToCatalogFile()
		}
		go s.triggerNomadDeploy()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": "Sunucu kurulumu ve başlatma işlemi tetiklendi.",
		})
		return
	}

	healthy, _ := s.probeShardLiveStatus(row)
	if !healthy {
		if action == "players" && r.Method == http.MethodGet && len(parts) == 2 {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":        true,
				"shardId":   row.ID,
				"operating": false,
				"deploying": true,
				"healthy":   false,
				"players":   []any{},
			})
			return
		}
		writeError(w, http.StatusConflict, "Shard sunucusu henüz hazırlanıyor (Deploying). Sunucu Healthy durumuna geçene kadar işlem yapılamaz.")
		return
	}

	switch action {
	case "settings":
		s.handleUpdateShardSettings(w, r, row)
	case "gms":
		s.handleShardGMs(w, r, accountID, row, parts[2:])
	case "bans":
		s.handleShardBans(w, r, accountID, row, parts[2:])
	case "players":
		if len(parts) >= 3 && parts[2] == "intervene" {
			s.handleShardPlayerAction(w, r, accountID, row)
			return
		}
		s.handleShardPlayers(w, r, row)
	case "player-action":
		s.handleShardPlayerAction(w, r, accountID, row)
	case "broadcast":
		s.handleShardBroadcast(w, r, row)
	default:
		writeError(w, http.StatusNotFound, "Bilinmeyen shard işlemi.")
	}
}

/*
================
handleUpdateShardSettings
================
*/
func (s *panelServer) handleUpdateShardSettings(w http.ResponseWriter, r *http.Request, row auth.ShardRow) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req struct {
		Name      string `json:"name"`
		Capacity  int    `json:"capacity"`
		CheckMode bool   `json:"checkMode"`
		Operating *bool  `json:"operating"`
		MaxLevel  int    `json:"maxLevel"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Geçersiz istek formatı.")
		return
	}
	checkMode := req.CheckMode
	if req.Operating != nil {
		checkMode = !*req.Operating
	}
	if err := s.accounts.UpdateShardOwnerSettings(r.Context(), row.ID, req.Name, req.Capacity, checkMode, req.MaxLevel); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.saveDatabaseToCatalogFile()

	if req.MaxLevel >= 1 && req.MaxLevel <= 140 {
		capVal := int64(req.MaxLevel)
		_, _ = s.callShardPlayerOperation(row, map[string]any{
			"id":       newOperationRequestID(),
			"operator": "panel",
			"action":   "set-level-cap",
			"maxLevel": capVal,
			"reason":   "Panel max level update",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "Shard ayarları güncellendi.",
	})
}

/*
================
handleShardGMs
================
*/
func (s *panelServer) handleShardGMs(w http.ResponseWriter, r *http.Request, accountID string, row auth.ShardRow, rest []string) {
	if !s.isShardOwnerOrAdmin(accountID, row) {
		writeError(w, http.StatusForbidden, "Yalnızca shard sahibi GM atayabilir veya kaldırabilir.")
		return
	}
	switch r.Method {
	case http.MethodPost:
		var req struct {
			AccountID string `json:"accountId"`
			UserID    string `json:"userId"`
			Action    string `json:"action"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "Hesap adı gerekli.")
			return
		}
		target := strings.TrimSpace(req.UserID)
		if target == "" {
			target = strings.TrimSpace(req.AccountID)
		}
		if target == "" {
			writeError(w, http.StatusBadRequest, "Hesap adı gerekli.")
			return
		}
		if strings.EqualFold(req.Action, "revoke") {
			if err := s.accounts.RevokeShardGM(r.Context(), target, row.ID); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		} else {
			if err := s.accounts.GrantShardGM(r.Context(), target, row.ID); err != nil {
				writeError(w, http.StatusBadRequest, "Belirtilen hesap bulunamadı: "+err.Error())
				return
			}
		}
		gms, _ := s.accounts.ListShardGMs(row.ID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "gms": gms})

	case http.MethodDelete:
		target := ""
		if len(rest) > 0 {
			target = strings.TrimSpace(rest[0])
		}
		if target == "" {
			writeError(w, http.StatusBadRequest, "Kaldırılacak GM hesabı belirtilmedi.")
			return
		}
		if err := s.accounts.RevokeShardGM(r.Context(), target, row.ID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		gms, _ := s.accounts.ListShardGMs(row.ID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "gms": gms})

	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

/*
================
handleShardBans
================
*/
func (s *panelServer) handleShardBans(w http.ResponseWriter, r *http.Request, accountID string, row auth.ShardRow, rest []string) {
	switch r.Method {
	case http.MethodPost:
		var req struct {
			AccountID string `json:"accountId"`
			UserID    string `json:"userId"`
			Action    string `json:"action"`
			Reason    string `json:"reason"`
			Character string `json:"character"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "Banlanacak hesap adı gerekli.")
			return
		}
		target := strings.TrimSpace(req.UserID)
		if target == "" {
			target = strings.TrimSpace(req.AccountID)
		}
		if target == "" {
			writeError(w, http.StatusBadRequest, "Banlanacak hesap adı gerekli.")
			return
		}
		if strings.EqualFold(req.Action, "unban") {
			if err := s.accounts.UnbanOnShard(r.Context(), row.ID, target); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			bans, _ := s.accounts.ListShardBans(row.ID)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bans": bans})
			return
		}
		if strings.EqualFold(target, row.OwnerAccountID) {
			writeError(w, http.StatusForbidden, "Shard sahibi banlanamaz.")
			return
		}
		if err := s.accounts.BanOnShard(r.Context(), row.ID, target, req.Reason); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if charName := strings.TrimSpace(req.Character); charName != "" {
			_, _ = s.callShardPlayerOperation(row, map[string]any{
				"id":        newOperationRequestID(),
				"operator":  accountID,
				"character": charName,
				"action":    "kick",
				"reason":    "Shard ban: " + req.Reason,
			})
		}
		bans, _ := s.accounts.ListShardBans(row.ID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bans": bans})

	case http.MethodDelete:
		target := ""
		if len(rest) > 0 {
			target = strings.TrimSpace(rest[0])
		}
		if target == "" {
			writeError(w, http.StatusBadRequest, "Banı kaldırılacak hesap belirtilmedi.")
			return
		}
		if err := s.accounts.UnbanOnShard(r.Context(), row.ID, target); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		bans, _ := s.accounts.ListShardBans(row.ID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bans": bans})

	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

/*
================
ensureShardOperatorToken
================
*/
func ensureShardOperatorToken(authorityDir string) (string, error) {
	tokenPath := filepath.Join(authorityDir, "operator-token")
	if raw, err := os.ReadFile(tokenPath); err == nil {
		if token := strings.TrimSpace(string(raw)); len(token) >= 32 {
			return token, nil
		}
	}
	if err := os.MkdirAll(authorityDir, 0o700); err != nil {
		return "", err
	}
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf[:])
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

/*
================
triggerNomadDeploy
================
*/
func (s *panelServer) triggerNomadDeploy() {
	s.deployRunning.Add(1)
	defer s.deployRunning.Add(-1)

	s.deployMu.Lock()
	defer s.deployMu.Unlock()

	bin := "sro-nomad"
	if runtime.GOOS == "windows" {
		bin = "sro-nomad.exe"
	}
	binPath := filepath.Join(s.moduleRoot, bin)
	if _, err := os.Stat(binPath); err != nil {
		return
	}

	env := os.Environ()
	if paths, err := gamedata.Resolve(); err == nil {
		env = append(env, gamedata.EnvRoot+"="+paths.RuntimeRoot)
	}
	if strings.TrimSpace(os.Getenv("SRO_AGENT_POSTGRES_DSN")) == "" {
		if raw, err := os.ReadFile(filepath.Join(s.stateDir, "postgres-dsn.txt")); err == nil {
			if dsn := strings.TrimSpace(string(raw)); dsn != "" {
				env = append(env, "SRO_AGENT_POSTGRES_DSN="+dsn)
			}
		}
	}

	deployArgs := []string{
		"deploy",
		"-agent-cpu=100",
		"-agent-memory-mb=128",
		"-gameworld-cpu=200",
		"-gameworld-memory-mb=1024",
	}
	if strings.TrimSpace(os.Getenv("SRO_DEV_PORT_OFFSET")) != "" {
		deployArgs = append(
			deployArgs,
			"-agent-port=8787",
			"-agent-provisioning-port=8789",
			"-allowed-origins=http://127.0.0.1:5180,http://localhost:5180",
		)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		cmd := exec.CommandContext(ctx, binPath, deployArgs...)
		cmd.Dir = s.moduleRoot
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		cancel()
		if err == nil {
			fmt.Fprintf(os.Stdout, "sro-panel: nomad deploy succeeded (attempt %d)\n", attempt)
			return
		}
		fmt.Fprintf(os.Stderr, "sro-panel: nomad deploy attempt %d error: %v\n%s\n", attempt, err, out.String())
		time.Sleep(5 * time.Second)
	}
}
