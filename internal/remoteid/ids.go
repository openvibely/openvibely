// Package remoteid formats IDs of entities owned by other saved servers. A remote
// ID is "r-<server>-<id>"; store calls carrying one run on that server.
package remoteid

import "strings"

const idPrefix = "r-"

// Prefix returns the ID prefix for a saved server.
func Prefix(serverID string) string { return idPrefix + serverID + "-" }

// ID tags a server-local ID with its server.
func ID(serverID, id string) string {
	if id == "" || strings.HasPrefix(id, idPrefix) {
		return id
	}
	return Prefix(serverID) + id
}

// Split returns the server and server-local ID of a remote ID.
func Split(id string) (serverID, local string, ok bool) {
	if len(id) < 19 || !strings.HasPrefix(id, idPrefix) || id[18] != '-' || !isHex(id[2:18]) {
		return "", id, false
	}
	return id[2:18], id[19:], true
}

// ServerOf reports the server of a remote ID.
func ServerOf(id string) (string, bool) {
	s, _, ok := Split(id)
	return s, ok
}

// FindServer returns the first server prefix contained anywhere in s.
func FindServer(s string) (string, bool) {
	for i := strings.Index(s, idPrefix); i >= 0; {
		if len(s)-i >= 19 && s[i+18] == '-' && isHex(s[i+2:i+18]) && (i == 0 || !isAlnum(s[i-1])) {
			return s[i+2 : i+18], true
		}
		next := strings.Index(s[i+1:], idPrefix)
		if next < 0 {
			break
		}
		i += next + 1
	}
	return "", false
}

// Strip removes one server's prefix everywhere in s.
func Strip(serverID, s string) string {
	if !strings.Contains(s, idPrefix) {
		return s
	}
	return strings.ReplaceAll(s, Prefix(serverID), "")
}

// IsEntityID reports whether s has the shape of an OpenVibely entity ID.
func IsEntityID(s string) bool { return len(s) == 32 && isHex(s) }

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isHexByte(s[i]) {
			return false
		}
	}
	return s != ""
}

func isHexByte(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' }

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}
