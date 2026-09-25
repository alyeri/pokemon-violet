# Mystery Gift through local BCAT

Pokémon Violet 3.0.1 can list and redeem Generation 9 WC9 cards from a
Ryujinx profile's local `bcat-seed/normal/distribution_internet` file. This is
local BCAT delivery; it does not require the NPLN backend to generate cards.

A four-card catalog was validated with Great Balls, Ultra Balls, a Ground Tera
Type Gyarados and the Floral-print Sports Backpack. The player redeemed all
four. A read-only inspection of the save found corresponding new Mystery Gift
receipt records while preserving preceding records.

Five more Violet item cards were generated from a separately held archival
multi-item WC9 template: 50 Master Balls, 500 Rare Candies, 100 Ability
Patches, 500 of each of the 18 ordinary Tera Shards, and 500 Stellar Tera
Shards. A nine-card catalog containing the four earlier cards and these five
loaded in the player-2 profile. The player saw and redeemed all five new
gifts. Exact final bag quantities have not been independently inspected.

An initial generated catalog failed to load because the edited cards retained
their template checksum. The builder now recalculates CRC-16/CCITT-FALSE at
offset `0x2C4`; the installer rejects invalid checksums before writing. Each
WC9 record is 712 bytes. The corrected nine-card catalog passed checksum
verification and the in-game listing and redemption test.

To build the cards, supply your own valid item WC9 template:

```powershell
pwsh -NoProfile -File build-violet-op-gifts.ps1 `
  -TemplatePath private/cards/item-template.wc9 `
  -OutputDirectory private/cards/op-gifts
```

To install, pass an array of WC9 paths and the root of the chosen Ryujinx
profile to `install-violet-mystery-gift.ps1`. Back up the profile's
existing `distribution_internet` file before changing it. Neither the cards
nor Pokémon game content are included in this repository.
