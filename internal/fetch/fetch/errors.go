package fetch

import "fmt"

// ErrTooLarge is returned when the response body exceeds the configured limit.
type ErrTooLarge struct {
	URL   string
	Limit int64
}

func (e *ErrTooLarge) Error() string {
	return fmt.Sprintf("too_large: response from %s exceeds %d bytes", e.URL, e.Limit)
}
