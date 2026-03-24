package queue

import (
	"icloud-reminders/internal/store"
)

func LockPath() string {
	return store.LockPath()
}

func WithQueueLock(fn func() error) error {
	return store.WithMutationLock(fn)
}
