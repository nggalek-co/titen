// Package locales registers Titen's message catalog and resolves the
// application language. Keys are looked up with Catalog.Format; a missing
// key returns the key itself, so the source language is the fallback.
package locales

import (
	"embed"

	"github.com/egoist/mygo/i18n"
)

//go:embed *.json
var files embed.FS

// Catalog holds the messages in en, the source language, and id.
var Catalog *i18n.Catalog

func init() {
	var err error
	if Catalog, err = i18n.NewCatalog(files, "en"); err != nil {
		panic(err)
	}
	i18n.Register(Catalog)
}

// Languages are the saved setting values; "system" follows the OS.
const (
	System   = "system"
	English  = "en"
	Indonesian = "id"
)

// Tag resolves a saved choice to the tag mygo.App.SetLocale expects.
func Tag(choice string) string {
	if choice == System {
		return ""
	}
	return choice
}

// T formats key in the app's current locale.
func T(key string) string {
	return Catalog.Format(i18n.Locale(), key, nil)
}

// Tf formats key with ICU MessageFormat args.
func Tf(key string, args map[string]any) string {
	return Catalog.Format(i18n.Locale(), key, args)
}
