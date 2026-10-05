package cmdutil

func ShortLogValue(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}
