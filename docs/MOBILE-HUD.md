# Mobile HUD Design (`feat/mobile-hud`)

Port-only, owner-approved mobile interface design for touch phones in landscape orientation.

## 1. Current State: Why Compact HUD Feels Poor on Phones

Today, viewports smaller than `800×600` activate the compact HUD (`apps/client-next/src/engine/runtime/ui/hud/compact-hud.ts`). Compact HUD compresses the desktop layout rather than rethinking it for two-thumb mobile play:

| # | Problem | Root Cause in Code | Impact |
| --- | --- | --- | --- |
| 1 | Bottom bar consumes ~28% of screen height | 11 quickslots (`44 px`) + `20 px` EXP strip + 11 tool buttons (`40 px`) + margins = `112 px` (`compact-hud.ts:15-60`) | On a `393 px` high screen, only `281 px` remains for the 3D world |
| 2 | Minimap is hidden by default | Only drawn when `compact.overlay === "map"`, and scaled down via `fitUiGroup` into remaining vertical space (`ui.ts:12714-12823`) | Players cannot continuously see their position, nearby entities, or quest markers |
| 3 | Single mutually exclusive overlay | Chat, system log, minimap, pets, and extended bar close one another (`compact-hud.ts:66-110`) | Reading chat hides the minimap; checking pets hides chat |
| 4 | Windows are scaled down instead of reflowed | `fitUiGroup` scales windows like `388×408` Inventory to `~0.69` | Bitmap fonts shrink to `~8 px`; touch targets drop below `30 px` |
| 5 | No virtual joystick for continuous movement | Only tap-to-walk exists; single-finger drag orbits the camera (`touch-camera.ts`) | Continuous kiting, fleeing, and tracking moving monsters is fatiguing |
| 6 | Weak touch targeting and attack loop | Native attack starts on double-click (`platform.ts:564-592`), with no touch equivalent for `Tab` target cycling or `F1–F12` | Core combat is the hardest action to perform on a phone |
| 7 | Notch and safe areas are ignored | No `viewport-fit=cover`; HUD never reads `env(safe-area-inset-*)` | Landscape iPhones lose `~59 px` on each side (`~14%` of the display) to black bars |
| 8 | Most of the 34 keyboard shortcuts have no direct touch access | Keys such as `C`, `I`, `S`, `Q`, `P`, `G` (`input-options.ts`) | Opening common windows requires nested menu taps |
| 9 | Party and buff boards remain at desktop coordinates | No mobile placement for `GDR_QUICKPARTYBOARD` or `GDR_MAGICSTATEBOARD`, and `partyOverlay`'s `230 px` desktop bottom reserve forces horizontal column wrapping on `393 px` screens | Top-left and top-center HUD elements collide |
| 10 | Portrait layout is cramped | Manifest locks `landscape`, while a portrait browser tab wraps quickslots into 2 rows | Low priority because the installed PWA locks to landscape |

### Preserved Foundations

- Single-finger camera orbit and two-finger pinch zoom (`touch-camera.ts`).
- `500 ms` long-press right-click activation and touch tooltip inspection (`touch-inspect.ts`).
- Enlarged `40×40 px` hit targets on close buttons (`compactCloseControl`).
- Active drag gesture protection (`drag-shield.ts`).
- Standalone iOS rotation visualViewport settlement (`fitCanvasViewport` in `video-options.ts`).
- GPU quad rendering paired with invisible DOM hit/accessibility controls (`platform/ui/ui.ts`).

---

## 2. Mobile HUD Architecture

Principle: a two-thumb landscape mobile MMO layout. The center of the screen remains unobstructed for the 3D world, persistent status lives in the upper corners, and active combat/movement controls live in the lower thumb arcs.

```text
┌──────────────────────────────────────────────────────────────────────┐
│[≡]  [Portrait] HP ████████░░          [TARGET: Mangyang  ████░]╭────╮│
│      Lv 54     MP █████░░░░                                    │MINI││
│      ○○○○○  (buff icons)                                       │MAP ││
│  [P1][P2][P3] (party, narrow)                                  ╰────╯│
│                                                               [+][-] │
│                                                          (X,Y) Jangan│
│                                                                      │
│                       ( W O R L D   —   clear view )                 │
│                                                                      │
│  ╭───────╮                                            [S1] [S2] [S3] │
│  │   ◉   │  (joystick, bottom-left,                  [S4]        [S5]│
│  │ ◉ ◉ ◉ │   appears at touch point)                   ┌────────┐[S6]│
│  ╰───────╯                                             │ ATTACK │    │
│  [💬 Ali: hello...] (chat preview)            [⇄target]└────────┘[📦]│
│▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ EXP 43.2% ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁│
└──────────────────────────────────────────────────────────────────────┘
```

### Regions

1. **Top-Right Pinned Minimap**:
   - Always visible in the top-right safe corner at 1:1 native scale (`GDR_MINIMAP`, circular mask `GDR_MINIMAP_ALPHA` `105×105 px`).
   - Reuses existing tile streaming, entity/quest/hunting markers, and zoom logic.
   - Displays zone name, `(X, Y)` coordinates, dungeon floor indicator, and touch-sized `+`/`-` zoom controls.
   - Tapping the circular minimap disc or the map toggle button opens the World Map (`toggle-window:Map`).
2. **Top-Left Character, Buff & Party Cluster**:
   - Character portrait, HP/MP bars, level, and berserk indicator (`GDR_PLAYER_MINI_INFO`) inset by `safe-area-inset-left` and `safe-area-inset-top`, with attack-pet mini info (`GDR_PMI_PET_MINI_INFO`) directly underneath when summoned.
   - Active buff and abnormal icons (`GDR_MAGICSTATEBOARD`) flow in a compact 6-column grid immediately below the player/pet panel instead of desktop's `x = 220` header position.
   - Quick-party slots (`GDR_QUICKPARTYBOARD`) stack vertically in a single narrow left column beneath the buff icons without triggering desktop's `230 px` bottom-reserve horizontal wrap.
3. **Top-Center Target Frame**:
   - Centered in the band between the top-left player cluster and the top-right minimap (`compactTargetStatus`), keeping its target buff row directly underneath.
4. **Bottom-Left Virtual Joystick + Tap-to-Walk (M3)**:
   - Floating virtual joystick in the bottom-left thumb zone wired to the existing directional walk input (`input.ts`), alongside world tap-to-walk.
5. **Bottom-Right Combat & Skill Cluster (M3)**:
   - Primary Attack button surrounded by 6 skill slots, quickslot page cycling, target cycling, and item pickup.
6. **Bottom Edge EXP Strip & Chat Preview (M5)**:
   - Full-width `4 px` EXP bar along the bottom safe edge, with a 1–2 line chat preview box above it that opens the chat drawer when tapped.
7. **Main Menu & Full-Height Panel Drawers (M4)**:
   - `4×2` icon grid (`≡`) for Inventory, Character, Skills, Quests, Party, Guild, Mall, and Options; windows open at `>= 0.9` scale in a right-hand drawer.
8. **Safe Areas (`viewport-fit=cover`)**:
   - Canvas covers the full physical display while HUD regions inset by `env(safe-area-inset-top/right/bottom/left)`.

### Viewport Comparison (`852×393` Landscape Phone)

| Metric | Legacy Compact HUD | Mobile HUD |
| --- | --- | --- |
| Unobstructed world height | `281 px` (`72%`) | `~385 px` (`98%`, after M3/M5 thin EXP strip) |
| Active canvas width | `~734 px` (side black bars) | `852 px` (`viewport-fit=cover`) |
| Minimap | Hidden behind overlay toggle | Always open in top-right corner |
| Window scale | `~0.69` | `>= 0.9` (M4) |

---

## 3. Implementation Roadmap

| Stage | Scope | Status |
| --- | --- | --- |
| **M0** | Baseline metrics and safe-area geometry contract | Done |
| **M1** | `viewport-fit=cover`, safe-area CSS/platform plumbing, pure `mobile-hud.ts` layout module + unit tests, Experimental `mobileHud` option (default on for touch phones) | Done |
| **M2** | Always-visible top-right minimap + top status cluster (safe-inset player/pet panel, top-center target frame, left-column buffs and party list) | Done |
| **M3** | Floating virtual joystick + bottom-right attack/skill/target/pickup cluster + thin bottom EXP strip + chat preview | Done |
| **M4** | Main menu (`≡`) 4×2 touch grid + right-hand panel drawers (`>= 0.9` scale, enlarged touch tabs/controls, Inventory & Character first) | Done |
| **M5** | Extended chat preview & mobile notifications | Planned |
| **M6** | Device verification across iPhone and Android aspect ratios | Planned |

---

## 4. Owner Decisions (2026-10-26)

1. **Experimental Flag (`mobileHud`)**: Listed in the Experimental window (`Other` tab: `"Mobile HUD"`). Enabled by default (`record.mobileHud !== false`); active on landscape touch screens smaller than `800×600`. Turning it off restores the legacy compact HUD.
2. **Movement**: Virtual joystick and tap-to-walk remain active together.
3. **Orientation**: Landscape only (`width > height` when `< 800×600`).

