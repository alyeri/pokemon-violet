package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"

	authpb "npln.nintendo.net/npln-practice/proto/auth/v1"
)

// Explicit local-development identities, never enabled by the normal token path.
func localVioletPlayer(ext *authpb.ExternalIdToken) (uint64, bool) {
	if !allowUnverified() || os.Getenv("VIOLET_LOCAL_TWO_PLAYERS") != "1" || ext == nil {
		return 0, false
	}
	parts := strings.Split(ext.GetNsaIdToken(), ".")
	if len(parts) != 3 {
		return 0, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, false
	}
	var claims struct {
		Player string `json:"violet_local_player"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return 0, false
	}
	switch claims.Player {
	case "1":
		return 1800000001, true
	case "2":
		return 1800000002, true
	default:
		return 0, false
	}
}
