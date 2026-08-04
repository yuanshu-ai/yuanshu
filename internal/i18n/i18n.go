// Package i18n provides generated, stable user-facing product messages.
package i18n

const (
	Chinese = "zh-CN"
	English = "en-US"
)

func ValidLocale(locale string) bool { return locale == Chinese || locale == English }

func Text(locale, key string) string {
	if !ValidLocale(locale) {
		locale = Chinese
	}
	if value := Catalogs[locale][key]; value != "" {
		return value
	}
	return key
}
