package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type recordingAppCleaner struct {
	appIDs []int64
	err    error
}

func (c *recordingAppCleaner) CleanupApp(_ context.Context, appID int64) error {
	c.appIDs = append(c.appIDs, appID)
	return c.err
}

func TestAppResourceCleanersRunsEveryProviderAndJoinsErrors(t *testing.T) {
	first := &recordingAppCleaner{err: errors.New("first provider unavailable")}
	second := &recordingAppCleaner{err: errors.New("second provider unavailable")}
	cleaners := appResourceCleaners{first, second}

	err := cleaners.CleanupApp(context.Background(), 42)
	if len(first.appIDs) != 1 || first.appIDs[0] != 42 || len(second.appIDs) != 1 || second.appIDs[0] != 42 {
		t.Fatalf("cleanup calls = first %v, second %v", first.appIDs, second.appIDs)
	}
	if err == nil || !strings.Contains(err.Error(), "first provider unavailable") || !strings.Contains(err.Error(), "second provider unavailable") {
		t.Fatalf("CleanupApp error = %v", err)
	}
}
