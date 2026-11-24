package nodeadmin

import "errors"

// ErrStoreRequired is returned by New when Options.Store is nil
var ErrStoreRequired = errors.New("nodeadmin: store is required")
