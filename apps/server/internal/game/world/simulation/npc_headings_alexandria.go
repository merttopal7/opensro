/*
===========================================================================

npc_headings_alexandria.go - reviewed facing for the optional Alexandria city

Port-only, not native: these exact placements are imported by SRO_ALEXANDRIA.
ISRO-R Tab_RefNest supplies headings for 36 placements, including Viviana's
nest 37159 and Sharon's nest 37158. Four event NPCs retain VSRO directions.
Join by codename and exact position, never by cross-version reference id.

ISRO-R shard SHA256:
89c6835f8e51b1a75950242c89cf3ad639ea7d50a22191a90632cca333664a45
VSRO shard SHA256:
b4d85a043ebce36c8704768dce0dfa63ff9418c8489241d8441b7902e28fa37a

===========================================================================
*/
package simulation

/*
================
alexandriaNPCHeadings

Exact placement keys keep the native roster unchanged when the overlay is off.
================
*/
var alexandriaNPCHeadings = map[npcHeadingKey]uint16{
	{"NPC_BATTLE_ARENA_MANAGER", 23088, 7805, 8620, 9456}:      57343,
	{"NPC_CH_EVENT_KISAENG1", 23088, 4498, 8620, 12695}:        40959,
	{"NPC_CH_GACHA_MACHINE", 23088, 13644, 8634, 11308}:        23665,
	{"NPC_CH_GACHA_OPERATOR", 23088, 7862, 8634, 12625}:        7827,
	{"NPC_SD_MERCHANT_CHANGER", 23344, 11374, 9360, 16084}:     32767,
	{"NPC_SD_M_AREA_ACCESSORY", 23088, 12453, 8634, 10539}:     23483,
	{"NPC_SD_M_AREA_AHHA", 23345, 8095, 14010, 10816}:          43690,
	{"NPC_SD_M_AREA_ARMOR", 23087, 17319, 8633, 8750}:          7827,
	{"NPC_SD_M_AREA_EYPTSOLIDER_1", 23343, 14409, 9631, 1745}:  59163,
	{"NPC_SD_M_AREA_EYPTSOLIDER_2", 23343, 15469, 9630, 2811}:  59163,
	{"NPC_SD_M_AREA_EYPTSOLIDER_3", 22833, 4712, 7919, 10019}:  21845,
	{"NPC_SD_M_AREA_EYPTSOLIDER_4", 23603, 11798, 15595, 9899}: 38228,
	{"NPC_SD_M_AREA_EYPTSOLIDER_5", 23603, 13270, 15600, 7599}: 38410,
	{"NPC_SD_M_AREA_EYPTSOLIDER_6", 22833, 6677, 7907, 11993}:  21845,
	{"NPC_SD_M_AREA_FERRY", 23856, 16232, 5849, 17948}:         49515,
	{"NPC_SD_M_AREA_GUILD", 23344, 6438, 9300, 14720}:          0,
	{"NPC_SD_M_AREA_HORSE", 23089, 8678, 8565, 16441}:          21845,
	{"NPC_SD_M_AREA_HUNTER", 23344, 7922, 9300, 9793}:          0,
	{"NPC_SD_M_AREA_LENENUNE", 22831, 17164, 9498, 19034}:      8009,
	{"NPC_SD_M_AREA_LIGHTHOUSE", 24112, 2867, 5847, 4706}:      0,
	{"NPC_SD_M_AREA_MANETO", 23345, 6510, 14010, 11667}:        43690,
	{"NPC_SD_M_AREA_MERCHANT", 23600, 7986, 9307, 1129}:        0,
	{"NPC_SD_M_AREA_POTION", 23088, 7970, 8633, 2628}:          23665,
	{"NPC_SD_M_AREA_SENMUTE", 23343, 13444, 9785, 3778}:        59163,
	{"NPC_SD_M_AREA_SMITH", 23087, 15728, 8633, 10699}:         8191,
	{"NPC_SD_M_AREA_SPECIAL", 23344, 11123, 9301, 19194}:       32585,
	{"NPC_SD_M_AREA_WAREHOUSE", 23089, 3353, 7777, 7987}:       23483,
	{"NPC_SD_M_AREA_WATERMAN_1", 24111, 18581, 5374, 2364}:     30583,
	{"NPC_SD_M_AREA_WATERMAN_1", 24112, 14552, 5358, 1554}:     364,
	{"NPC_SD_M_AREA_WATERMAN_2", 23857, 7048, 5519, 5445}:      182,
	{"NPC_SD_M_AREA_WATERMAN_2", 24111, 14702, 5450, 5630}:     27670,
	{"NPC_SD_M_AREA_WATERMAN_2", 24112, 16895, 5193, 1570}:     33131,
	{"NPC_SD_THIEF_AGENT_CHANGER", 23346, 17028, 14036, 12213}: 18750,
	{"NPC_SD_T_AREA_ACCESSORY", 23602, 12252, 14479, 5292}:     52428,
	{"NPC_SD_T_AREA_ARMOR", 23602, 6392, 13646, 875}:           61530,
	{"NPC_SD_T_AREA_GUILD2", 23347, 1943, 14029, 14478}:        27124,
	{"NPC_SD_T_AREA_POTION", 23602, 8295, 14077, 3462}:         56432,
	{"NPC_SD_T_AREA_SMITH", 23346, 6484, 13110, 17305}:         0,
	{"NPC_SD_T_AREA_THIEF", 23347, 3612, 14616, 18498}:         31129,
	{"NPC_SD_T_AREA_WAREHOUSE2", 23603, 4622, 15302, 2406}:     19842,
}
