package plus

import (
	"os/exec"
	"testing"
)

func TestCompanionCannotStartAfterShutdown(t *testing.T) {
	c := NewCompanion(func() { t.Error("an unstarted companion triggered exit") })
	c.Show()
	c.Stop()
	c.Stop()
	if err := c.Start(false); err == nil || c.command != nil {
		t.Fatal("shutdown allowed a new companion process")
	}
}

func TestCompanionCannotReplaceItsRunningProcess(t *testing.T) {
	c := NewCompanion(nil)
	original := &exec.Cmd{}
	c.command = original
	if err := c.Start(true); err == nil || c.command != original {
		t.Fatal("a second start replaced the companion process")
	}
}
