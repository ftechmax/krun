package sessionkey

import "strings"

const Default = "__default__"

func Trim(value string) string {
	return strings.TrimSpace(value)
}

func Normalize(value string) string {
	trimmed := Trim(value)
	if trimmed == "" {
		return Default
	}
	return trimmed
}

func IsBlank(value string) bool {
	return Trim(value) == ""
}
