package kybstore

import "github.com/Angle-HR/server/internal/verifyqueue"

// Queue is the review queue, now owned by the identity service's admin module (internal/verifyqueue). The alias
// keeps org's enqueue call compiling until org calls IdentityService.EnqueueVerification instead.
type Queue = verifyqueue.Queue

// ErrNoPendingItem means there was no open review item to resolve.
var ErrNoPendingItem = verifyqueue.ErrNoPendingItem
