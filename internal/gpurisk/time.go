package gpurisk

import "gorm.io/gorm"

// SQLite stores legacy timestamps with mixed UTC/local offsets. Its text
// ordering is not chronological. SQL performs a coarse inclusive prefilter;
// callers then verify exact boundaries in Go (SQLite julianday loses sub-ms).
// column/op are private fixed schema identifiers, never HTTP/CLI input.
func timeSQL(db *gorm.DB, column, op string) string {
	if db.Dialector.Name() == "sqlite" {
		if op == "<" {
			op = "<="
		}
		return "julianday(" + column + ") " + op + " julianday(?)"
	}
	return column + " " + op + " ?"
}

func timeOrder(db *gorm.DB, column string) string {
	if db.Dialector.Name() == "sqlite" {
		return "julianday(" + column + ") DESC, id DESC"
	}
	return column + " DESC, id DESC"
}
