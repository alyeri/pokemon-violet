# Pokémon Violet

Experimental Nextendo NPLN server for Pokémon Violet 3.0.1.

This is a work in progress. The backend has been tested with Ryujinx-Nextendo
profiles on one Windows PC. Validated local flows include Union Circle, Link
Trade, Surprise Trade, Rental Teams and Ranked Singles. Ranked Singles completes
matchmaking, peer-to-peer setup, a full battle, consensus result reporting and
persistent rank updates. Link Battle and Casual Singles have reached a real
battle.

The current build also hosts a public Tera Raid room. A player completed the
raid solo and caught its Pokémon. The Poké Portal raid board makes successful
distributed and ordinary session queries. Mystery Gift loads a nine-card BCAT
catalog; all five locally generated rare-item cards were listed and redeemed.
Official Competition registration displays an initial rating of 1500.

Multiplayer raid discovery and joining, Ranked Doubles and Casual Doubles
matches, completed Official battles, wider concurrency and internet deployment
still require testing. See [Tera Raid status](docs/tera-raids.md) and
[Mystery Gift BCAT](docs/mystery-gift-bcat.md).

## Build

Use Go 1.26.4 or a compatible newer release:

```sh
go build -o violet-server .
```

Copy `example.env` into your environment and replace every placeholder. The
server requires your own Nextendo TLS/CA material and account-service secrets.
Ranked regulation loading also requires a legally obtained, decompressed
Pokémon Violet 3.0.1 `main` image supplied through
`VIOLET_RANKED_MAIN_IMAGE`. No game files, keys, certificates or account data
are included.

## Tests

Go test sources are kept in `Test Files`. Run them in an isolated temporary
copy of the backend:

```powershell
powershell -NoProfile -File ".\Test Files\run-tests.ps1"
```

or on a Unix-like system:

```sh
sh "Test Files/run-tests.sh"
```

## Client patches

`client patches` contains the Ryujinx-Nextendo changes used during local
development. Apply only patches that match the client revision you are
building. The repository does not include a client binary.

## Mystery Gift cards

`scripts/build-violet-op-gifts.ps1` builds five item WC9 cards from a separately
obtained, valid Scarlet/Violet item-card template. It recalculates each WC9
checksum. `scripts/install-violet-mystery-gift.ps1` validates card structure,
checksum and duplicate IDs before installing a merged catalog in a selected
Ryujinx profile. The repository includes neither a template nor gift binaries.

Based on [Nextendo Network](https://github.com/NextendoNetwork)'s service
layout and public NPLN protocol definitions. See `LICENSE` for terms.
