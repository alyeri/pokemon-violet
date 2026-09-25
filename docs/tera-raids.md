# Tera Raid status

The backend accepts Violet 3.0.1 `CreateGameSessionCreationTicket` requests for
`RaidPublic` and `QueryGameSessions` requests for `RaidPublicSearch`. A public
room has four participant slots and retains the raid properties provided by
the host. Search returns only public `RaidPublic` rooms with matching
properties, sufficient vacancy and a visible session.

In a local player-2 test, **Challenge as a group → Let anyone join** reached
the Tera Raid waiting room. The player started the battle, completed it solo
and caught Swalot; the game displayed its Pokédex registration. This validates
hosting and the solo battle path with the current backend.

The Poké Portal raid board then issued two `RaidPublicSearch` queries for
four-star raids: first `is_distributed=1`, then `is_distributed=0`. Both returned
successfully with no postings because the hosted room had already ended. The
search page size was 20. A future test needs a second client searching while a
host room is live, followed by joining and completing a multiplayer battle.

The raid board lists live player-hosted rooms. BCAT event raid data is a
separate local distribution mechanism; placing event data in BCAT does not
publish persistent rooms on the board. A catalog of every possible raid would
require valid live sessions or additional host-side behavior and has not been
implemented.

The `RaidPublic` random matchmaking ticket is accepted, but its end-to-end
join behavior is not yet validated in game. Tests cover empty search, host
creation, public room discovery and exclusion of unrelated room types.
