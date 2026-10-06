package backup

import "github.com/freefsm-project/freefsm/internal/backupactivity"

// Aliases keep the public engine contract independent of activity persistence.
// Shared types avoid the backup test -> database -> services -> backup cycle.
type ActivityActor = backupactivity.Actor
type ActivityEvent = backupactivity.Event
