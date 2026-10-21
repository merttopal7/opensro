# Pet skill icons

The three `pet-skills*.png` atlases are original AI-generated artwork created
for the pet expansion.
Native character skill icons supplied the visual reference, not the subjects.
No retail PK2 is edited.

Each atlas has five equal columns (the two active and three passive
rows) and four equal rows: Wolf, Bear, Penguin, Crow. Book I uses the simpler
`pet-skills-1.png`, book II uses
`pet-skills-2.png`, and book III retains the unchanged `pet-skills.png`.
Row-help icons use the book III symbol at20px. `pet-skill-icons.ts` selects
cells using normalized UVs.

`buildPetItemIcons()` publishes this source through full CIF and focused
`pnpm assets refresh pet-loot` builds, with the normal publication ledger and
UI icon packs. Set `SRO_GAME_ROOT` to your licensed extracted client root.

Top-right badges show server-observed Resolve, bear retaliation hits or crow
burst progress. Mark Prey shows attacks remaining to recharge. The left row
help also shows the owner's independent retaliation count. These are transient
combat values, not saved skill ranks or client-predicted animation counts.

## Pet equipment icons

`pet-gear-collar.png`, `pet-gear-harness.png` and `pet-gear-charm.png` are
original painted3x3 sheets, degrees1..9 in reading order. The normal
`scripts/build/shared/pet_item_icons.py` export converts each cell to32px RGBA.
Native character necklaces/rings provided style references, not copied subjects.
All existing logical item icon paths stay unchanged, so both player and pet
inventories use the new artwork after normal asset publication/client reload.
