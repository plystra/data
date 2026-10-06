package declaration

// ValidMemberID reports whether value is a canonical versioned Data member ID.
func ValidMemberID(value string) bool {
	return validMemberID(value)
}
