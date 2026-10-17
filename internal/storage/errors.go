package storage

import "errors"

// Sentinel errors returned by scope and lifecycle operations. HTTP and query
// layers map these to stable error codes without inspecting free-form strings.
var (
	// ErrScopeEmpty is returned when selected_collections is requested with no
	// collection ids.
	ErrScopeEmpty = errors.New("scope_empty")
	// ErrForbidden is returned when a scope references a collection outside the
	// workspace or a deleted/invisible collection.
	ErrForbidden = errors.New("forbidden")
	// ErrConflict is returned when a state transition is not allowed.
	ErrConflict = errors.New("conflict")
)
