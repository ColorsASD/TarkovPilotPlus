//go:build windows

package plus

import (
	"testing"
	"time"
)

func TestInspectClickWaitsForUIAndCoalescesDoubleClick(t *testing.T) {
	start := time.Unix(1000, 0)
	var gate inspectClickGate
	if gate.ready(start.Add(time.Hour)) {
		t.Fatal("time passing without a click triggered a scan")
	}
	gate.release(start)
	if gate.ready(start.Add(199 * time.Millisecond)) {
		t.Fatal("scan ran before the Inspect UI could open")
	}
	gate.release(start.Add(150 * time.Millisecond))
	if gate.ready(start.Add(200 * time.Millisecond)) {
		t.Fatal("double click was not allowed to finish")
	}
	if !gate.ready(start.Add(350 * time.Millisecond)) {
		t.Fatal("completed double click did not trigger the Inspect check")
	}
	if gate.ready(start.Add(time.Hour)) {
		t.Fatal("one click caused repeated automatic scans")
	}
	gate.release(start.Add(500 * time.Millisecond))
	if gate.ready(start.Add(849 * time.Millisecond)) {
		t.Fatal("rapid clicks bypassed the 500ms throttle")
	}
	if !gate.ready(start.Add(850 * time.Millisecond)) {
		t.Fatal("a click during the throttle was discarded rather than delayed")
	}
}

func TestSlowDoubleClickStillChecksNewlyOpenedInspect(t *testing.T) {
	start := time.Unix(1000, 0)
	var gate inspectClickGate
	gate.release(start)
	if !gate.ready(start.Add(200 * time.Millisecond)) {
		t.Fatal("first click was not checked")
	}
	// Windows can accept a double click after the first probe already found no
	// Inspect window. Its second release must remain pending during throttling.
	gate.release(start.Add(300 * time.Millisecond))
	if gate.ready(start.Add(500 * time.Millisecond)) {
		t.Fatal("the second probe bypassed throttling")
	}
	if !gate.ready(start.Add(700 * time.Millisecond)) {
		t.Fatal("the second release opening Inspect was lost")
	}
}
