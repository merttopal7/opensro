/*
===========================================================================

db_write.go - normalized authority writes within the caller's transaction

===========================================================================
*/
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"opensro.online/server/internal/domain"
)

// Transaction writers translate dirty in-memory authority records into the
// normalized SQLite layout. They never own transaction lifecycle.
/*
================
upsertMetaTx
================
*/
func upsertMetaTx(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec("INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

// upsertCharacterTx writes one character row inside a transaction.
/*
================
upsertCharacterTx
================
*/
func upsertCharacterTx(tx *sql.Tx, division string, c *domain.Character) error {
	record, err := json.Marshal(c)
	if err != nil {
		// Plain-data structs do not fail to marshal; when one does, the
		// whole commit fails LOUD instead of silently dropping a
		// character row (the same posture the JSON engine had).
		return fmt.Errorf("marshaling character %s/%s: %w", division, c.Name, err)
	}
	_, err = tx.Exec(
		"INSERT INTO characters (division, id, name_lower, record) VALUES (?, ?, ?, ?) ON CONFLICT(division, id) DO UPDATE SET name_lower = excluded.name_lower, record = excluded.record",
		division, c.ID, strings.ToLower(c.Name), string(record))
	return err
}

/*
================
groundItemRecordEqual

Reports whether two persisted ground-item records are identical on disk.
Checks frequently mutated fields (OwnerJID, StackCount) first and avoids
reflection unless a companion summon is attached.
================
*/
func groundItemRecordEqual(a, b domain.GroundItemRecord) bool {
	if a.Gid != b.Gid ||
		a.OwnerJID != b.OwnerJID ||
		a.StackCount != b.StackCount ||
		a.RefObjID != b.RefObjID ||
		a.GoldAmount != b.GoldAmount ||
		a.DroppedAtMs != b.DroppedAtMs ||
		a.RecordID != b.RecordID ||
		a.PopulationWorld != b.PopulationWorld ||
		a.TransformRefObjID != b.TransformRefObjID ||
		a.TypeFlags != b.TypeFlags ||
		a.Plus != b.Plus ||
		a.Durability != b.Durability ||
		a.RegionID != b.RegionID ||
		a.X != b.X ||
		a.Y != b.Y ||
		a.Z != b.Z ||
		a.Heading != b.Heading ||
		a.Codename != b.Codename ||
		a.DroppedBy != b.DroppedBy ||
		a.VarianceBits != b.VarianceBits ||
		a.TradeOwner != b.TradeOwner ||
		!slices.Equal(a.MagicOptions, b.MagicOptions) {
		return false
	}
	if a.Summon == nil && b.Summon == nil {
		return true
	}
	return reflect.DeepEqual(a.Summon, b.Summon)
}

/*
================
upsertGroundItemTx
================
*/
func upsertGroundItemTx(tx *sql.Tx, divisionID string, item domain.GroundItemRecord) error {
	record, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("marshaling ground %s/gid %d: %w", divisionID, item.Gid, err)
	}
	_, err = tx.Exec(
		"INSERT INTO ground_items (division, gid, record) VALUES (?, ?, ?) ON CONFLICT(division, gid) DO UPDATE SET record = excluded.record",
		divisionID, item.Gid, string(record),
	)
	return err
}

/*
================
syncGroundDeltaTx

Applies the O(delta) difference between previous (the last committed ground
plane on disk) and current (the new ground snapshot) inside a transaction.
Unchanged ground items are neither re-marshaled nor re-inserted, keeping
commits O(dropped + picked + expired) regardless of how many items lie on
the ground across the shard.
================
*/
func syncGroundDeltaTx(tx *sql.Tx, previous, current map[string][]domain.GroundItemRecord) error {
	divisionSet := make(map[string]struct{}, len(previous)+len(current))
	for divisionID := range previous {
		divisionSet[divisionID] = struct{}{}
	}
	for divisionID := range current {
		divisionSet[divisionID] = struct{}{}
	}
	divisionIDs := make([]string, 0, len(divisionSet))
	for divisionID := range divisionSet {
		divisionIDs = append(divisionIDs, divisionID)
	}
	sort.Strings(divisionIDs)

	for _, divisionID := range divisionIDs {
		prevRows := previous[divisionID]
		currRows := current[divisionID]
		if len(currRows) == 0 {
			if len(prevRows) > 0 {
				if _, err := tx.Exec("DELETE FROM ground_items WHERE division = ?", divisionID); err != nil {
					return err
				}
			}
			continue
		}

		i, j := 0, 0
		for i < len(prevRows) && j < len(currRows) {
			if prevRows[i].Gid < currRows[j].Gid {
				if _, err := tx.Exec("DELETE FROM ground_items WHERE division = ? AND gid = ?", divisionID, prevRows[i].Gid); err != nil {
					return err
				}
				i++
				continue
			}
			if prevRows[i].Gid > currRows[j].Gid {
				if err := upsertGroundItemTx(tx, divisionID, currRows[j]); err != nil {
					return err
				}
				j++
				continue
			}
			if !groundItemRecordEqual(prevRows[i], currRows[j]) {
				if err := upsertGroundItemTx(tx, divisionID, currRows[j]); err != nil {
					return err
				}
			}
			i++
			j++
		}
		for ; i < len(prevRows); i++ {
			if _, err := tx.Exec("DELETE FROM ground_items WHERE division = ? AND gid = ?", divisionID, prevRows[i].Gid); err != nil {
				return err
			}
		}
		for ; j < len(currRows); j++ {
			if err := upsertGroundItemTx(tx, divisionID, currRows[j]); err != nil {
				return err
			}
		}
	}
	return nil
}

// replaceMailboxTx replaces ONE character's mailbox rows inside a
// transaction. A mailbox is capped at the client panel's 20 rows, so a
// whole-mailbox replace on dirty commits is O(small) like the ground
// plane, and it keeps seq == list position - the wire-index contract the
// letter lane's u8 indices rely on.
/*
================
replaceMailboxTx
================
*/
func replaceMailboxTx(tx *sql.Tx, division string, charID int64, mailbox []domain.LetterRecord) error {
	if _, err := tx.Exec("DELETE FROM memos WHERE division = ? AND char_id = ?", division, charID); err != nil {
		return err
	}
	for seq, letter := range mailbox {
		record, err := json.Marshal(letter)
		if err != nil {
			return fmt.Errorf("marshaling memo %s/%d[%d]: %w", division, charID, seq, err)
		}
		if _, err := tx.Exec("INSERT INTO memos (division, char_id, seq, record) VALUES (?, ?, ?, ?)", division, charID, seq, string(record)); err != nil {
			return err
		}
	}
	return nil
}

// replaceGuildTx replaces ONE guild's rows - the guild row plus its whole
// member set - inside a transaction. The dirty unit is the whole guild
// (guildKey), and a member set is bounded by the client's u8 member count,
// so the whole-set replace is O(small) like a mailbox, and it keeps
// seq == list position - the wire order the 0x32C4 member loop emits.
/*
================
replaceGuildTx
================
*/
func replaceGuildTx(tx *sql.Tx, division string, guildID int64, guild domain.GuildRecord, members []domain.GuildMemberRecord) error {
	if _, err := tx.Exec("DELETE FROM guilds WHERE division = ? AND guild_id = ?", division, guildID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM guild_members WHERE division = ? AND guild_id = ?", division, guildID); err != nil {
		return err
	}
	record, err := json.Marshal(guild)
	if err != nil {
		return fmt.Errorf("marshaling guild %s/%d: %w", division, guildID, err)
	}
	if _, err := tx.Exec("INSERT INTO guilds (division, guild_id, record) VALUES (?, ?, ?)", division, guildID, string(record)); err != nil {
		return err
	}
	for seq, member := range members {
		memberRecord, err := json.Marshal(member)
		if err != nil {
			return fmt.Errorf("marshaling guild member %s/%d[%d]: %w", division, guildID, seq, err)
		}
		if _, err := tx.Exec("INSERT INTO guild_members (division, guild_id, char_id, record, seq) VALUES (?, ?, ?, ?, ?)", division, guildID, member.CharID, string(memberRecord), seq); err != nil {
			return err
		}
	}
	return nil
}

// deleteGuildTx removes ONE guild's rows - the guild row plus its whole
// member set - inside a transaction (replaceGuildTx's DELETE half with
// no re-insert: the dissolution door's persistence action).
/*
================
deleteGuildTx
================
*/
func deleteGuildTx(tx *sql.Tx, division string, guildID int64) error {
	if _, err := tx.Exec("DELETE FROM guilds WHERE division = ? AND guild_id = ?", division, guildID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM guild_members WHERE division = ? AND guild_id = ?", division, guildID); err != nil {
		return err
	}
	return nil
}

// replaceCampTx replaces ONE training camp's rows - the camp row plus its
// whole member set - inside a transaction (replaceGuildTx's twin; the
// member set is bounded by the client's 8-row roster assert
// @0x0082917b "TraningCampMember is Over than 8", so the whole-set
// replace is O(small), and seq == list position is the wire order the
// 0x3AC5 status-10 sub-1 roster loop emits).
/*
================
replaceCampTx
================
*/
func replaceCampTx(tx *sql.Tx, division string, campID int64, camp domain.TrainingCampRecord, members []domain.TrainingCampMemberRecord) error {
	if _, err := tx.Exec("DELETE FROM training_camps WHERE division = ? AND camp_id = ?", division, campID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM training_camp_members WHERE division = ? AND camp_id = ?", division, campID); err != nil {
		return err
	}
	record, err := json.Marshal(camp)
	if err != nil {
		return fmt.Errorf("marshaling training camp %s/%d: %w", division, campID, err)
	}
	if _, err := tx.Exec("INSERT INTO training_camps (division, camp_id, record) VALUES (?, ?, ?)", division, campID, string(record)); err != nil {
		return err
	}
	for seq, member := range members {
		memberRecord, err := json.Marshal(member)
		if err != nil {
			return fmt.Errorf("marshaling training camp member %s/%d[%d]: %w", division, campID, seq, err)
		}
		if _, err := tx.Exec("INSERT INTO training_camp_members (division, camp_id, char_id, record, seq) VALUES (?, ?, ?, ?, ?)", division, campID, member.CharID, string(memberRecord), seq); err != nil {
			return err
		}
	}
	return nil
}

// deleteCampTx removes ONE training camp's rows - the camp row plus its
// whole member set - inside a transaction (replaceCampTx's DELETE half with
// no re-insert: the DisbandCamp door's persistence action).
/*
================
deleteCampTx
================
*/
func deleteCampTx(tx *sql.Tx, division string, campID int64) error {
	if _, err := tx.Exec("DELETE FROM training_camps WHERE division = ? AND camp_id = ?", division, campID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM training_camp_members WHERE division = ? AND camp_id = ?", division, campID); err != nil {
		return err
	}
	return nil
}

// seedAuthorityDBTx populates a freshly created current-schema database.
// Transaction ownership stays with the caller so publishing cannot expose a
// partial seed.
/*
================
seedAuthorityDBTx
================
*/
func seedAuthorityDBTx(tx *sql.Tx, data *authoritySeed) error {
	divisionSet := map[string]bool{}
	for divisionID := range data.characters {
		divisionSet[divisionID] = true
	}
	for divisionID := range data.deleted {
		divisionSet[divisionID] = true
	}
	for divisionID := range data.ground {
		divisionSet[divisionID] = true
	}
	divisionIDs := make([]string, 0, len(divisionSet))
	for divisionID := range divisionSet {
		divisionIDs = append(divisionIDs, divisionID)
	}
	sort.Strings(divisionIDs)

	for _, divisionID := range divisionIDs {
		for _, c := range data.characters[divisionID] {
			if err := upsertCharacterTx(tx, divisionID, c); err != nil {
				return err
			}
		}
		for i, raw := range data.deleted[divisionID] {
			if _, err := tx.Exec("INSERT INTO deleted_characters (division, seq, record) VALUES (?, ?, ?)", divisionID, i, string(raw)); err != nil {
				return err
			}
		}
		for _, item := range data.ground[divisionID] {
			record, err := json.Marshal(item)
			if err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO ground_items (division, gid, record) VALUES (?, ?, ?)", divisionID, item.Gid, string(record)); err != nil {
				return err
			}
		}
	}
	for divisionID, next := range data.meta.NextCharID {
		if _, err := tx.Exec("INSERT INTO next_char_id (division, next_id) VALUES (?, ?) ON CONFLICT(division) DO UPDATE SET next_id = excluded.next_id", divisionID, next); err != nil {
			return err
		}
	}
	if err := upsertMetaTx(tx, metaKeySchemaVersion, fmt.Sprintf("%d", CurrentVersion)); err != nil {
		return err
	}
	if err := upsertMetaTx(tx, metaKeyLayoutVersion, fmt.Sprintf("%d", CurrentLayoutVersion)); err != nil {
		return err
	}
	if err := upsertMetaTx(tx, metaKeyGidCounter, fmt.Sprintf("%d", data.meta.GidCounter)); err != nil {
		return err
	}
	if err := writeTradeRewards(tx, data.meta.TradeRewards); err != nil {
		return err
	}
	if err := writeJobRankings(tx, data.meta.JobRankings); err != nil {
		return err
	}
	return upsertMetaTx(tx, metaKeyUpdatedAtMs, fmt.Sprintf("%d", data.updatedAt))
}
