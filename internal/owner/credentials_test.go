package owner

import "testing"

// A relay dial that began before Leave (or this device's removal) cleared the
// owner's trust asks for credentials after: none, rather than a crash
// (hermeshd panicked here when a removed computer was set up again).
func TestCredentialsAfterLeave(t *testing.T) {
	o := &Owner{}
	c := o.credentials()
	if c.GetRootChain() != nil || c.GetDeviceCert() != nil {
		t.Fatalf("credentials without trust = %v, want none", c)
	}
}
