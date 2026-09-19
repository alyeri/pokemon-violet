# Pokémon Violet

Experimental Nextendo NPLN server for Pokémon Violet 3.0.1.

This is a work in progress. The current backend has been tested with two
Ryujinx-Nextendo profiles on the same Windows PC. The validated local flows
include Union Circle, Link Trade, Surprise Trade, Rental Teams and Ranked
Singles. Ranked Singles completes matchmaking, peer-to-peer setup, a full
battle, consensus result reporting and persistent rank updates. Link Battle
and Battle Stadium Casual Singles have been validated through entry into a
real battle.

Ranked Doubles, Casual Doubles, Online Competitions, wider concurrency and
internet deployment still require testing.

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

Based on [Nextendo Network](https://github.com/NextendoNetwork)'s service
layout and public NPLN protocol definitions. See `LICENSE` for terms.
