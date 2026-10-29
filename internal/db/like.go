package db

import "strings"

// likeEscaper escapes LIKE's wildcards with the character every substring
// search here declares, as `LIKE ? ESCAPE '\'`.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// containsPattern is the LIKE pattern matching text that contains s, taken
// literally: a search for "50%" or "user_id" means those characters, not "50
// followed by anything" or "user, any one character, id".
func containsPattern(s string) string {
	return "%" + likeEscaper.Replace(s) + "%"
}
