package config

import (
	"fmt"
)

// ExampleSecret_String demonstrates calling Secret.String on a
// concrete literal input, verifying that it returns the redaction marker.
func ExampleSecret_String() {
	s := Secret("S" + "A3...secret...value")
	fmt.Println(s.String())
	// Output:
	// [REDACTED]
}
