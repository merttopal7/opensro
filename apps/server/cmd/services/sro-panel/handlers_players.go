/*
===========================================================================

handlers_players.go - Live online player inspection and interventions

Bridges the Customer & Shard Management Console to each shard's GameWorld
operator API and global account Silk authority.

===========================================================================
*/
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	agentapi "opensro.online/server/internal/agent/api"
	"opensro.online/server/internal/security/auth"
)

/*
================
shardPlayerEntry
================
*/
type shardPlayerEntry struct {
	CharID     int64  `json:"charId"`
	CharName   string `json:"charName"`
	UserID     string `json:"userId"`
	Level      int    `json:"level"`
	Gold       int64  `json:"gold"`
	SkillPoint int64  `json:"skillPoint"`
	SilkOwn    int64  `json:"silkOwn"`
	RegionID   int    `json:"regionId"`
	RegionName string `json:"regionName"`
	Online     bool   `json:"online"`
}

/*
================
probeShardLiveStatus

Verifies both the GameWorld transport readiness gate (/transport/readyz, the
same endpoint Nomad's readiness check requires for a Healthy allocation) and
the operator control API before reporting the shard as healthy/running.
================
*/
func (s *panelServer) probeShardLiveStatus(row auth.ShardRow) (bool, int) {
	transportBase := strings.TrimRight(strings.TrimSpace(row.TransportURL), "/")
	if transportBase == "" {
		return false, -1
	}
	readyReq, err := http.NewRequest(http.MethodGet, transportBase+"/transport/readyz", nil)
	if err != nil {
		return false, -1
	}
	readyResp, err := s.httpClient.Do(readyReq)
	if err != nil {
		return false, -1
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(readyResp.Body, 512))
	_ = readyResp.Body.Close()
	if readyResp.StatusCode != http.StatusOK {
		return false, -1
	}

	rosterRaw, err := s.fetchShardPlayerRead(row, "")
	if err == nil {
		if m, ok := rosterRaw.(map[string]any); ok {
			if chars, ok := m["characters"].([]any); ok {
				online := 0
				for _, item := range chars {
					if cm, ok := item.(map[string]any); ok {
						if b, ok := cm["online"].(bool); ok && b {
							online++
						}
					}
				}
				return true, online
			}
		}
		return true, -1
	}
	if _, obsErr := s.fetchShardObservatory(row); obsErr == nil {
		return true, -1
	}
	return false, -1
}

/*
================
regionDisplayName
================
*/
func regionDisplayName(regionID int) string {
	switch regionID {
	case 25000, 25001, 24744:
		return "Jangan"
	case 26265:
		return "Donwhang"
	case 23687:
		return "Hotan"
	case 27244:
		return "Samarkand"
	case 26959:
		return "Constantinople"
	case 23088:
		return "Alexandria"
	default:
		if regionID > 0 {
			return fmt.Sprintf("Bölge #%d", regionID)
		}
		return "Jangan"
	}
}

/*
================
handleShardPlayers
================
*/
func (s *panelServer) handleShardPlayers(w http.ResponseWriter, r *http.Request, row auth.ShardRow) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	character := strings.TrimSpace(r.URL.Query().Get("character"))
	if character != "" {
		detail, err := s.fetchShardPlayerRead(row, character)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"player": detail,
		})
		return
	}

	obsData, obsErr := s.fetchShardObservatory(row)
	rosterData, rosterErr := s.fetchShardPlayerRead(row, "")

	players := make([]shardPlayerEntry, 0)
	if m, ok := rosterData.(map[string]any); ok {
		if chars, ok := m["characters"].([]any); ok {
			for idx, item := range chars {
				cm, ok := item.(map[string]any)
				if !ok {
					continue
				}
				charName, _ := cm["name"].(string)
				accountID, _ := cm["accountId"].(string)
				charID := int64(idx + 1)
				if idFloat, ok := cm["id"].(float64); ok && idFloat > 0 {
					charID = int64(idFloat)
				}
				level := 1
				if lvFloat, ok := cm["level"].(float64); ok && lvFloat > 0 {
					level = int(lvFloat)
				}
				var gold int64
				if gFloat, ok := cm["gold"].(float64); ok {
					gold = int64(gFloat)
				}
				var sp int64
				if spFloat, ok := cm["skillPoint"].(float64); ok {
					sp = int64(spFloat)
				}
				var regionID int
				if regFloat, ok := cm["regionId"].(float64); ok {
					regionID = int(regFloat)
				}
				online, _ := cm["online"].(bool)
				var silkOwn int64
				if accountID != "" {
					if accInfo, accErr := s.accounts.Lookup(accountID); accErr == nil {
						silkOwn = accInfo.Silk.Own
					}
				}
				players = append(players, shardPlayerEntry{
					CharID:     charID,
					CharName:   charName,
					UserID:     accountID,
					Level:      level,
					Gold:       gold,
					SkillPoint: sp,
					SilkOwn:    silkOwn,
					RegionID:   regionID,
					RegionName: regionDisplayName(regionID),
					Online:     online,
				})
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"shardId":     row.ID,
		"operating":   row.Operating && (obsErr == nil || rosterErr == nil),
		"players":     players,
		"observatory": obsData,
		"roster":      rosterData,
		"obsError":    errString(obsErr),
		"rosterError": errString(rosterErr),
	})
}

/*
================
handleShardBroadcast
================
*/
func (s *panelServer) handleShardBroadcast(w http.ResponseWriter, r *http.Request, row auth.ShardRow) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Geçersiz istek formatı.")
		return
	}
	msg := strings.TrimSpace(req.Message)
	if err := auth.ValidateNoticeText(msg); err != nil {
		writeError(w, http.StatusBadRequest, "Duyuru metni 1-100 karakter arasında olmalıdır.")
		return
	}
	if err := s.sendShardNotice(row, msg); err != nil {
		writeError(w, http.StatusBadGateway, "Duyuru gönderilemedi: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "Oyun içi GM duyurusu gönderildi.",
	})
}

/*
================
handleShardPlayerAction
================
*/
func (s *panelServer) handleShardPlayerAction(w http.ResponseWriter, r *http.Request, accountID string, row auth.ShardRow) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req struct {
		Action       string `json:"action"`
		Character    string `json:"character"`
		CharName     string `json:"charName"`
		UserID       string `json:"userId"`
		Town         uint32 `json:"town"`
		Level        *int64 `json:"level"`
		Gold         *int64 `json:"gold"`
		SkillPoint   *int64 `json:"skillPoint"`
		SP           *int64 `json:"sp"`
		Silk         uint32 `json:"silk"`
		SilkDelta    int64  `json:"silkDelta"`
		Codename     string `json:"codename"`
		ItemCodeName string `json:"itemCodeName"`
		Count        uint32 `json:"count"`
		ItemCount    uint32 `json:"itemCount"`
		Message      string `json:"message"`
		Reason       string `json:"reason"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Geçersiz istek formatı.")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if len(reason) < 5 {
		reason = "Panel GM mudahalesi (" + accountID + ")"
	}

	if req.Action == "notice" {
		msg := strings.TrimSpace(req.Message)
		if err := auth.ValidateNoticeText(msg); err != nil {
			writeError(w, http.StatusBadRequest, "Duyuru metni 1-100 karakter arasında olmalıdır.")
			return
		}
		if err := s.sendShardNotice(row, msg); err != nil {
			writeError(w, http.StatusBadGateway, "Duyuru gönderilemedi: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": "Oyun içi GM duyurusu gönderildi.",
		})
		return
	}

	character := strings.TrimSpace(req.CharName)
	if character == "" {
		character = strings.TrimSpace(req.Character)
	}
	if character == "" {
		writeError(w, http.StatusBadRequest, "Karakter adı gerekli.")
		return
	}

	payload := map[string]any{
		"id":        newOperationRequestID(),
		"operator":  accountID,
		"character": character,
		"reason":    reason,
	}

	switch req.Action {
	case "rescue", "unstuck":
		town := req.Town
		if town == 0 {
			town = 1
		}
		payload["action"] = "rescue"
		payload["town"] = town

	case "kick":
		payload["action"] = "kick"

	case "clear-pk", "clear_pk":
		payload["action"] = "clear-pk"

	case "reset-stats", "reset_stats":
		payload["action"] = "reset-stats"

	case "grant-silk", "add_silk":
		if req.SilkDelta != 0 && strings.TrimSpace(req.UserID) != "" {
			if _, err := s.accounts.AdjustSilk(r.Context(), strings.TrimSpace(req.UserID), req.SilkDelta, 0, 0); err != nil {
				writeError(w, http.StatusBadRequest, "Silk güncellenemedi: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		silkAmount := req.Silk
		if silkAmount == 0 && req.SilkDelta > 0 {
			silkAmount = uint32(req.SilkDelta)
		}
		if silkAmount == 0 {
			writeError(w, http.StatusBadRequest, "Eklenecek Silk miktarı 0'dan büyük olmalıdır.")
			return
		}
		payload["action"] = "grant-silk"
		payload["silk"] = silkAmount

	case "grant-items", "give_item":
		code := strings.TrimSpace(req.ItemCodeName)
		if code == "" {
			code = strings.TrimSpace(req.Codename)
		}
		count := req.ItemCount
		if count == 0 {
			count = req.Count
		}
		if count == 0 {
			count = 1
		}
		if code == "" {
			writeError(w, http.StatusBadRequest, "Eşya kodu (örn: ITEM_MALL_GLOBAL_CHATTING) gerekli.")
			return
		}
		payload["action"] = "grant-items"
		payload["items"] = []map[string]any{
			{"codename": code, "count": count},
		}

	case "set-character", "set_level", "set_gold", "set_sp":
		spVal := req.SkillPoint
		if spVal == nil {
			spVal = req.SP
		}
		if req.Level == nil && req.Gold == nil && spVal == nil {
			writeError(w, http.StatusBadRequest, "Level, Gold veya SP değerlerinden en az biri girilmelidir.")
			return
		}
		payload["action"] = "set-character"
		if req.Level != nil {
			payload["level"] = *req.Level
		}
		if req.Gold != nil {
			payload["gold"] = *req.Gold
		}
		if spVal != nil {
			payload["skillPoint"] = *spVal
		}

	default:
		writeError(w, http.StatusBadRequest, "Bilinmeyen oyuncu müdahale işlemi: "+req.Action)
		return
	}

	result, err := s.callShardPlayerOperation(row, payload)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"result": result,
	})
}

/*
================
fetchShardObservatory
================
*/
func (s *panelServer) fetchShardObservatory(row auth.ShardRow) (any, error) {
	target := strings.TrimSuffix(row.ControlURL, "/") + agentapi.ObservatoryPath
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-SRO-Local-Diagnostics", "1")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

/*
================
fetchShardPlayerRead
================
*/
func (s *panelServer) fetchShardPlayerRead(row auth.ShardRow, character string) (any, error) {
	authorityDir := filepath.Join(s.moduleRoot, ".state", "shards", row.ID, "authority")
	token, err := ensureShardOperatorToken(authorityDir)
	if err != nil {
		return nil, err
	}
	target := strings.TrimSuffix(row.ControlURL, "/") + "/internal/operations/player"
	if character != "" {
		target += "?character=" + character
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-SRO-Local-Diagnostics", "1")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

/*
================
callShardPlayerOperation
================
*/
func (s *panelServer) callShardPlayerOperation(row auth.ShardRow, payload map[string]any) (any, error) {
	authorityDir := filepath.Join(s.moduleRoot, ".state", "shards", row.ID, "authority")
	token, err := ensureShardOperatorToken(authorityDir)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	target := strings.TrimSuffix(row.ControlURL, "/") + "/internal/operations/player"
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-SRO-Local-Diagnostics", "1")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

/*
================
sendShardNotice
================
*/
func (s *panelServer) sendShardNotice(row auth.ShardRow, message string) error {
	signerPath := filepath.Join(s.stateDir, auth.AgentSessionPrivateKeyRingFile)
	signer, err := auth.NewAgentSessionSigner(signerPath)
	if err != nil {
		return err
	}
	token, err := signer.MintNotice(row.ID, message, time.Now())
	if err != nil {
		return err
	}
	target := strings.TrimSuffix(row.ControlURL, "/") + agentapi.NoticePath
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(token))
	if err != nil {
		return err
	}
	req.Header.Set("X-SRO-Local-Diagnostics", "1")
	req.Header.Set("Content-Type", "text/plain")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

/*
================
newOperationRequestID
================
*/
func (s *panelServer) newOpID() string {
	return newOperationRequestID()
}

func newOperationRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("panel_%d_%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}

/*
================
errString
================
*/
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
