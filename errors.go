package valve

import "errors"

var (
	// ErrStoreRequired is returned when a Limiter is created without a Store.
	ErrStoreRequired = errors.New("valve: store is required")

	// ErrInvalidRate is returned when the rate is less than or equal to zero.
	ErrInvalidRate = errors.New("valve: rate must be greater than zero")

	// ErrInvalidMaxTokens is returned when the maximum tokens is less than or equal to zero.
	ErrInvalidMaxTokens = errors.New("valve: max tokens must be greater than zero")

	// ErrInvalidRefillInterval is returned when the refill interval is less than or equal to zero.
	ErrInvalidRefillInterval = errors.New("valve: refill interval must be greater than zero")

	// ErrRateTooHigh is returned when the rate is too high for the refill interval (resulting in a zero token interval).
	ErrRateTooHigh = errors.New("valve: rate is too high for the refill interval")
)
